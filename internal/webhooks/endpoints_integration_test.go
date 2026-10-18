//go:build integration

package webhooks

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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

// registrar creates endpoints with a cap no test here reaches by accident.
func registrar(pool *pgxpool.Pool, keys *Keyring, policy TargetPolicy) Registrar {
	return Registrar{Pool: pool, Keys: keys, Policy: policy, MaxEndpoints: 100}
}

// warmPool opens every connection of the pool up front, so racing callers
// really run concurrently instead of queueing behind connection setup.
func warmPool(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	n := int(pool.Config().MaxConns)
	conns := make([]*pgxpool.Conn, 0, n)
	for range n {
		c, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Release()
	}
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
	pool, q, projectID := setup(t)
	keys := keyring(t, "k1:"+key(1))

	e, secret, err := registrar(pool, keys, lax).Create(ctx, projectID, "http://127.0.0.1:9000/hook?token=t0k", nil)
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

	only, _, err := registrar(pool, keys, lax).Create(ctx, projectID, "https://hooks.example.test/a",
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
		if _, _, err := registrar(pool, keys, tc.policy).Create(ctx, projectID, tc.url, tc.types); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := registrar(pool, nil, lax).Create(ctx, projectID, "https://hooks.example.test/", nil); !errors.Is(err, ErrNoKeys) {
		t.Errorf("no keys: err = %v", err)
	}
	if _, _, err := registrar(pool, keys, lax).Create(ctx, uuid.New(), "https://hooks.example.test/", nil); !errors.Is(err, ErrUnknownProject) {
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
	e, secret, err := registrar(pool, keys, lax).Create(ctx, projectID, "https://hooks.example.test/", nil)
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
		e, s, err := registrar(pool, old, lax).Create(ctx, projectID, "https://hooks.example.test/", nil)
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
		e, _, err := registrar(pool, k1, lax).Create(ctx, projectID, "https://hooks.example.test/", nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	// Created last, so it sorts last: encrypted under a key that the
	// re-encryption below does not know.
	lost, _, err := registrar(pool, keyring(t, "k0:"+key(9)), lax).Create(ctx, projectID, "https://hooks.example.test/", nil)
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

// The cap counts active and disabled endpoints of the project only, and
// holds when creates race.
func TestCreateEndpointCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, q, projectID := setup(t)
	keys := keyring(t, "k1:"+key(1))
	capped := Registrar{Pool: pool, Keys: keys, Policy: lax, MaxEndpoints: 3}

	first, _, err := capped.Create(ctx, projectID, "https://hooks.example.test/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetEndpointActive(ctx, pool, projectID, first.ID, false); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, _, err := capped.Create(ctx, projectID, "https://hooks.example.test/n", nil); err != nil {
			t.Fatalf("create %d: %v", i+2, err)
		}
	}
	if _, _, err := capped.Create(ctx, projectID, "https://hooks.example.test/over", nil); !errors.Is(err, ErrEndpointLimit) {
		t.Fatalf("create over the cap (one endpoint disabled): err = %v, want ErrEndpointLimit", err)
	}
	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := capped.Create(ctx, other.ID, "https://hooks.example.test/other", nil); err != nil {
		t.Fatalf("another project's endpoints count against this one: %v", err)
	}
	// Validation failures never count and never reach the database.
	if _, _, err := capped.Create(ctx, other.ID, "ftp://hooks.example.test/", nil); !errors.Is(err, ErrForbiddenTarget) {
		t.Fatalf("bad scheme: err = %v", err)
	}
	if _, _, err := capped.Create(ctx, other.ID, "https://hooks.example.test/", []string{"email.opened"}); !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("unknown type: err = %v", err)
	}
	if _, _, err := (Registrar{Pool: pool, Keys: keys, Policy: lax}).Create(ctx, other.ID, "https://hooks.example.test/", nil); err == nil {
		t.Fatal("a registrar without a cap accepted a create")
	}

	// Ten concurrent creates with one slot left: exactly one wins.
	racing, err := auth.CreateProject(ctx, q, "racing")
	if err != nil {
		t.Fatal(err)
	}
	ten := Registrar{Pool: pool, Keys: keys, Policy: lax, MaxEndpoints: 10}
	for range 9 {
		if _, _, err := ten.Create(ctx, racing.ID, "https://hooks.example.test/pre", nil); err != nil {
			t.Fatal(err)
		}
	}
	warmPool(t, pool)
	errs := make(chan error, 10)
	start := make(chan struct{})
	for range 10 {
		go func() {
			<-start
			_, _, err := ten.Create(ctx, racing.ID, "https://hooks.example.test/race", nil)
			errs <- err
		}()
	}
	close(start)
	var ok, limited int
	for range 10 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, ErrEndpointLimit):
			limited++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || limited != 9 {
		t.Fatalf("ok = %d, limited = %d; want 1 and 9", ok, limited)
	}
	if list, err := ListEndpoints(ctx, q, racing.ID); err != nil || len(list) != 10 {
		t.Fatalf("endpoints = %d, %v; want 10", len(list), err)
	}
}

// A held project lock makes a create give up after the lock timeout with
// ErrBusy instead of waiting or failing as an internal error.
func TestCreateEndpointBusy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, q, projectID := setup(t)
	reg := registrar(pool, keyring(t, "k1:"+key(1)), lax)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM projects WHERE id = $1 FOR NO KEY UPDATE", projectID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err = reg.Create(ctx, projectID, "https://hooks.example.test/", nil)
	waited := time.Since(start)
	_ = tx.Rollback(ctx)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("lock held: err = %v, want ErrBusy", err)
	}
	if waited < 2500*time.Millisecond || waited > 9500*time.Millisecond {
		t.Fatalf("gave up after %v, want about the 3s lock timeout", waited)
	}
	if list, _ := ListEndpoints(ctx, q, projectID); len(list) != 0 {
		t.Fatalf("busy create stored %d endpoints", len(list))
	}
	if _, _, err := reg.Create(ctx, projectID, "https://hooks.example.test/", nil); err != nil {
		t.Fatalf("after the lock is released: %v", err)
	}
}
