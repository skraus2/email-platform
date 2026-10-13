//go:build integration

package database_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/testdb"
)

func TestMigrationsAreReversibleAndGateReadiness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, err := database.Open(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m, err := database.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Fresh database: reachable but not migrated -> not ready.
	if err := database.Ready(ctx, pool, m); !errors.Is(err, database.ErrSchemaBehind) {
		t.Fatalf("Ready on empty db = %v, want ErrSchemaBehind", err)
	}

	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := database.Ready(ctx, pool, m); err != nil {
		t.Fatalf("Ready after Up = %v, want nil", err)
	}
	current, target, err := m.Versions(ctx)
	if err != nil || current != target || target < 1 {
		t.Fatalf("Versions = %d/%d (%v), want equal and >= 1", current, target, err)
	}

	// Every migration must roll back cleanly and re-apply.
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if err := database.Ready(ctx, pool, m); !errors.Is(err, database.ErrSchemaBehind) {
		t.Fatalf("Ready after DownTo(0) = %v, want ErrSchemaBehind", err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatalf("re-Up: %v", err)
	}

	// Up is idempotent.
	results, err := m.Up(ctx)
	if err != nil || len(results) != 0 {
		t.Fatalf("second Up applied %d migrations (%v), want 0", len(results), err)
	}
}

func TestReadyFailsWhenDatabaseUnreachable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Port 1 on loopback: nothing listens there.
	pool, err := database.Open(ctx, "postgres://u:p@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m, err := database.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	err = database.Ready(ctx, pool, m)
	if err == nil || errors.Is(err, database.ErrSchemaBehind) {
		t.Fatalf("Ready = %v, want a connection error", err)
	}
}

// Readiness probes run constantly and under a least-privilege role later; they
// must never write (e.g. create goose's version table) or take migration locks.
func TestReadinessCheckIsReadOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, err := database.Open(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m, err := database.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if err := database.Ready(ctx, pool, m); !errors.Is(err, database.ErrSchemaBehind) {
		t.Fatalf("Ready on empty db = %v, want ErrSchemaBehind", err)
	}
	var table *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('goose_db_version')::text").Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != nil {
		t.Fatalf("readiness check created %s; it must be read-only", *table)
	}
}
