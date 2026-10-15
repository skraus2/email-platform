//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

func TestCreateProjectAndKeyStoresOnlyHash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)

	p, err := auth.CreateProject(ctx, q, "billing-app")
	if err != nil {
		t.Fatal(err)
	}
	token, key, err := auth.CreateKey(ctx, q, p.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(key.Scopes) != 2 {
		t.Errorf("default scopes = %v", key.Scopes)
	}

	// The plaintext token or secret must not exist anywhere in the table.
	var hash []byte
	var prefix string
	if err := pool.QueryRow(ctx, "SELECT prefix, secret_hash FROM api_keys WHERE id = $1", key.ID).Scan(&prefix, &hash); err != nil {
		t.Fatal(err)
	}
	_, secret, err := auth.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(hash, []byte(secret)) {
		t.Fatal("secret stored in plaintext")
	}
	var leaks int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM api_keys WHERE position($1 in row(api_keys.*)::text) > 0", secret).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if leaks != 0 {
		t.Fatal("secret found in api_keys row")
	}
	if !auth.Matches(secret, hash) || prefix != key.Prefix {
		t.Fatal("stored hash/prefix do not match the issued token")
	}
}

func TestCreateKeyValidatesInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	q := store.New(testdb.NewMigrated(t))
	if _, _, err := auth.CreateKey(ctx, q, uuid.New(), nil); !errors.Is(err, auth.ErrProjectNotFound) {
		t.Fatalf("unknown project: err = %v, want ErrProjectNotFound", err)
	}
	p, _ := auth.CreateProject(ctx, q, "x")
	if _, _, err := auth.CreateKey(ctx, q, p.ID, []string{"emails:delete"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
	// The database accepts every known scope (the CHECK constraint follows).
	if _, _, err := auth.CreateKey(ctx, q, p.ID, auth.KnownScopes); err != nil {
		t.Fatalf("all known scopes: %v", err)
	}
	if _, err := auth.CreateProject(ctx, q, ""); err == nil {
		t.Fatal("empty project name accepted")
	}
}

func TestRevokeKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	q := store.New(testdb.NewMigrated(t))
	p, _ := auth.CreateProject(ctx, q, "x")
	_, key, err := auth.CreateKey(ctx, q, p.ID, []string{auth.ScopeEmailsRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.RevokeKey(ctx, q, key.Prefix); err != nil {
		t.Fatal(err)
	}
	if err := auth.RevokeKey(ctx, q, key.Prefix); !errors.Is(err, auth.ErrKeyNotFound) {
		t.Fatalf("second revoke err = %v, want ErrKeyNotFound", err)
	}
	keys, err := q.ListAPIKeys(ctx, p.ID)
	if err != nil || len(keys) != 1 || keys[0].RevokedAt == nil {
		t.Fatalf("keys = %+v (%v), want one revoked key", keys, err)
	}
}
