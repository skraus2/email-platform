// Package testdb gives integration tests an isolated, throwaway PostgreSQL
// database. Each call creates a uniquely named database on the server named by
// TEST_DATABASE_URL and drops it when the test ends, so tests never touch the
// development database and can run in parallel.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/database"
)

// EnvVar names the admin connection URL (any database on the test server; the
// role needs CREATEDB). Set by `make test-integration`.
const EnvVar = "TEST_DATABASE_URL"

// New creates an empty database and returns a pool connected to it.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), NewURL(t))
	if err != nil {
		// pgx parse errors can echo the URL incl. password; keep the message fixed.
		t.Fatal("testdb: cannot connect to the throwaway database")
	}
	t.Cleanup(pool.Close)
	return pool
}

const dropTimeout = time.Minute

// NewURL creates an empty database and returns its connection URL.
func NewURL(t testing.TB) string {
	t.Helper()
	adminURL := os.Getenv(EnvVar)
	if adminURL == "" {
		t.Fatalf("testdb: %s is not set; run `make test-integration` (needs `make up`)", EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("testdb: connect admin (is `make up` running?): %v", redact(err))
	}
	defer admin.Close(context.Background())

	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "ep_test_" + hex.EncodeToString(b[:])
	// name is generated from hex only, so identifier quoting is sufficient.
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("testdb: create database: %v", err)
	}

	t.Cleanup(func() {
		// DROP DATABASE forces a checkpoint; with many test packages writing
		// in parallel that can take well over 10 s.
		ctx, cancel := context.WithTimeout(context.Background(), dropTimeout)
		defer cancel()
		conn, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			t.Errorf("testdb: cleanup connect: %v", redact(err))
			return
		}
		defer conn.Close(context.Background())
		if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("testdb: drop database %s: %v", name, err)
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("testdb: parse %s", EnvVar)
	}
	u.Path = "/" + name
	return u.String()
}

// redact keeps connection errors useful (refused, auth failed) but drops
// config parse errors, which can contain the full connection string.
func redact(err error) error {
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return err
	}
	return errors.New("invalid TEST_DATABASE_URL or connection failure")
}

// NewMigrated creates a throwaway database with all migrations applied.
func NewMigrated(t testing.TB) *pgxpool.Pool {
	t.Helper()
	_, pool := NewMigratedURL(t)
	return pool
}

// NewMigratedURL is NewMigrated that also returns the database URL, for
// tests that start code which opens its own connection.
func NewMigratedURL(t testing.TB) (string, *pgxpool.Pool) {
	t.Helper()
	dbURL := NewURL(t)
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal("testdb: cannot connect to the throwaway database")
	}
	t.Cleanup(pool.Close)
	m, err := database.NewMigrator(pool)
	if err != nil {
		t.Fatalf("testdb: migrator: %v", err)
	}
	defer func() { _ = m.Close() }()
	if _, err := m.Up(context.Background()); err != nil {
		t.Fatalf("testdb: migrate: %v", err)
	}
	return dbURL, pool
}
