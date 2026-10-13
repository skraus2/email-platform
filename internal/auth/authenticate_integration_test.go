//go:build integration

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

func TestAuthenticateAgainstDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	q := store.New(testdb.NewMigrated(t))
	p, _ := auth.CreateProject(ctx, q, "app")
	token, key, err := auth.CreateKey(ctx, q, p.ID, []string{auth.ScopeEmailsRead})
	if err != nil {
		t.Fatal(err)
	}

	got, err := auth.Authenticate(ctx, q.GetActiveKeyByPrefix, token)
	if err != nil || got.ProjectID != p.ID || !got.Has(auth.ScopeEmailsRead) || got.Has(auth.ScopeEmailsSend) {
		t.Fatalf("active key: %+v, %v", got, err)
	}

	if err := auth.SetProjectStatus(ctx, q, p.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(ctx, q.GetActiveKeyByPrefix, token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("disabled project: err = %v, want ErrUnauthorized", err)
	}
	if err := auth.SetProjectStatus(ctx, q, p.ID, true); err != nil {
		t.Fatal(err)
	}

	if err := auth.RevokeKey(ctx, q, key.Prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(ctx, q.GetActiveKeyByPrefix, token); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("revoked key: err = %v, want ErrUnauthorized", err)
	}
}
