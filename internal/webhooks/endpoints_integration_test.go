//go:build integration

package webhooks

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

var lax = TargetPolicy{AllowHTTP: true, AllowPrivate: true}

func setup(t *testing.T) (*pgxpool.Pool, *store.Queries, uuid.UUID) {
	t.Helper()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	p, err := auth.CreateProject(context.Background(), q, "app")
	if err != nil {
		t.Fatal(err)
	}
	return pool, q, p.ID
}

func keyring(t *testing.T, spec string) *Keyring {
	t.Helper()
	k, err := ParseKeyring(spec)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func storedRow(t *testing.T, q *store.Queries, projectID, id uuid.UUID) store.WebhookEndpoint {
	t.Helper()
	rows, err := q.ListWebhookEndpoints(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("endpoint %s not stored in project %s", id, projectID)
	return store.WebhookEndpoint{}
}

func TestCreateAndListEndpoints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, q, projectID := setup(t)
	keys := keyring(t, "k1:"+key(1))

	e, secret, err := CreateEndpoint(ctx, q, keys, lax, projectID, "http://127.0.0.1:9000/hook?token=t0k", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.EventTypes, EventTypes) || e.Status != "active" || e.KeyID != "k1" {
		t.Fatalf("endpoint = %+v", e)
	}
	row := storedRow(t, q, projectID, e.ID)
	if bytes.Contains(row.SecretCiphertext, []byte(secret)) {
		t.Fatal("secret stored in plain text")
	}
	if got, err := secretOf(keys, row); err != nil || got != secret {
		t.Fatalf("stored secret decrypts to %q, %v", got, err)
	}

	only, _, err := CreateEndpoint(ctx, q, keys, lax, projectID, "https://hooks.example.test/a",
		[]string{"email.bounced", "email.complained", "email.bounced"})
	if err != nil || !slices.Equal(only.EventTypes, []string{"email.bounced", "email.complained"}) {
		t.Fatalf("subset = %+v, %v", only, err)
	}

	for name, tc := range map[string]struct {
		url    string
		types  []string
		policy TargetPolicy
	}{
		"unknown type":    {"https://hooks.example.test/", []string{"email.opened"}, lax},
		"http refused":    {"http://hooks.example.test/", nil, TargetPolicy{}},
		"private refused": {"https://10.0.0.1/", nil, TargetPolicy{}},
		"metadata":        {"https://169.254.169.254/", nil, lax},
	} {
		if _, _, err := CreateEndpoint(ctx, q, keys, tc.policy, projectID, tc.url, tc.types); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := CreateEndpoint(ctx, q, nil, lax, projectID, "https://hooks.example.test/", nil); !errors.Is(err, ErrNoKeys) {
		t.Errorf("no keys: err = %v", err)
	}
	if _, _, err := CreateEndpoint(ctx, q, keys, lax, uuid.New(), "https://hooks.example.test/", nil); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: err = %v", err)
	}

	list, err := ListEndpoints(ctx, q, projectID)
	if err != nil || len(list) != 2 || list[0].ID != e.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := ListEndpoints(ctx, q, other.ID); len(list) != 0 {
		t.Fatalf("other project sees %d endpoints", len(list))
	}
}

func TestEndpointStatusAndRotation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, q, projectID := setup(t)
	keys := keyring(t, "k1:"+key(1))
	e, secret, err := CreateEndpoint(ctx, q, keys, lax, projectID, "https://hooks.example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}

	if changed, err := SetEndpointActive(ctx, pool, projectID, e.ID, false); err != nil || !changed {
		t.Fatalf("disable = %v, %v", changed, err)
	}
	if changed, err := SetEndpointActive(ctx, pool, projectID, e.ID, false); err != nil || changed {
		t.Fatalf("disable twice = %v, %v", changed, err)
	}
	if storedRow(t, q, projectID, e.ID).Status != "disabled" {
		t.Fatal("not disabled")
	}
	if _, err := SetEndpointActive(ctx, pool, other.ID, e.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable via another project: err = %v", err)
	}
	if changed, err := SetEndpointActive(ctx, pool, projectID, e.ID, true); err != nil || !changed {
		t.Fatalf("enable = %v, %v", changed, err)
	}

	if _, err := RotateSecret(ctx, pool, keys, other.ID, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rotate via another project: err = %v", err)
	}
	if got, _ := secretOf(keys, storedRow(t, q, projectID, e.ID)); got != secret {
		t.Fatal("foreign rotation changed the secret")
	}
	rotated, err := RotateSecret(ctx, pool, keys, projectID, e.ID)
	if err != nil || rotated == secret {
		t.Fatalf("rotate = %q, %v", rotated, err)
	}
	if got, err := secretOf(keys, storedRow(t, q, projectID, e.ID)); err != nil || got != rotated {
		t.Fatalf("stored secret = %q, %v; want the rotated one", got, err)
	}
}

func TestReencryptMovesSecretsToActiveKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, q, projectID := setup(t)
	old := keyring(t, "k1:"+key(1))
	var secrets []string
	var ids []uuid.UUID
	for range 3 {
		e, s, err := CreateEndpoint(ctx, q, old, lax, projectID, "https://hooks.example.test/", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids, secrets = append(ids, e.ID), append(secrets, s)
	}

	// Without the old key the secrets cannot be read: nothing changes.
	if _, err := Reencrypt(ctx, pool, keyring(t, "k2:"+key(2))); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("missing old key: err = %v, want ErrUndecryptable", err)
	}
	if storedRow(t, q, projectID, ids[0]).SecretKeyID != "k1" {
		t.Fatal("partial re-encryption")
	}

	rotated := keyring(t, "k2:"+key(2)+",k1:"+key(1))
	n, err := Reencrypt(ctx, pool, rotated)
	if err != nil || n != 3 {
		t.Fatalf("reencrypt = %d, %v", n, err)
	}
	newOnly := keyring(t, "k2:"+key(2))
	for i, id := range ids {
		r := storedRow(t, q, projectID, id)
		if got, err := secretOf(newOnly, r); r.SecretKeyID != "k2" || err != nil || got != secrets[i] {
			t.Fatalf("endpoint %d: key %s, secret %q, %v", i, r.SecretKeyID, got, err)
		}
	}
	if n, err := Reencrypt(ctx, pool, rotated); err != nil || n != 0 {
		t.Fatalf("second run = %d, %v", n, err)
	}
}

// Re-encryption is all or nothing: a secret that cannot be decrypted, found
// after others were already re-encrypted, rolls all of them back.
func TestReencryptIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, q, projectID := setup(t)
	k1 := keyring(t, "k1:"+key(1))
	var ids []uuid.UUID
	for range 2 {
		e, _, err := CreateEndpoint(ctx, q, k1, lax, projectID, "https://hooks.example.test/", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	// Created last, so it sorts last: encrypted under a key that the
	// re-encryption below does not know.
	lost, _, err := CreateEndpoint(ctx, q, keyring(t, "k0:"+key(9)), lax, projectID, "https://hooks.example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reencrypt(ctx, pool, keyring(t, "k2:"+key(2)+",k1:"+key(1))); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("err = %v, want ErrUndecryptable", err)
	}
	for _, id := range ids {
		if kid := storedRow(t, q, projectID, id).SecretKeyID; kid != "k1" {
			t.Fatalf("endpoint %s moved to %s although the run failed", id, kid)
		}
	}
	if kid := storedRow(t, q, projectID, lost.ID).SecretKeyID; kid != "k0" {
		t.Fatalf("undecryptable endpoint changed to %s", kid)
	}
}
