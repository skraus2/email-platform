// Package database owns the PostgreSQL connection pool, schema migrations and
// the schema-version check used for readiness.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/skraus2/email-platform/db"
)

// ErrSchemaBehind means the database has not been migrated to the version this
// binary was built with. The process must not report ready in that state.
var ErrSchemaBehind = errors.New("database schema is behind the embedded migrations")

// Open creates a connection pool. It does not connect eagerly: a database that
// is down at startup shows up as "not ready", not as a crash loop.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx parse errors may echo the connection string; do not wrap them.
		return nil, errors.New("parse database config: invalid DATABASE_URL")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	return pool, nil
}

// Migrator applies and inspects the embedded goose migrations.
type Migrator struct {
	sqlDB    *sql.DB
	provider *goose.Provider
}

// NewMigrator builds a Migrator on top of an existing pool.
func NewMigrator(pool *pgxpool.Pool) (*Migrator, error) {
	migrations, err := fs.Sub(db.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	// Advisory lock: concurrent `migrate up` runs (e.g. two deploys) serialise
	// instead of racing on the same migration.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("create migration lock: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations, goose.WithSessionLocker(locker))
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("create migration provider: %w", err)
	}
	return &Migrator{sqlDB: sqlDB, provider: provider}, nil
}

// Up applies all pending migrations.
func (m *Migrator) Up(ctx context.Context) ([]*goose.MigrationResult, error) {
	return m.provider.Up(ctx)
}

// DownTo rolls back to the given version (0 = everything). Used by tests that
// prove migrations are reversible; not exposed as a CLI command.
func (m *Migrator) DownTo(ctx context.Context, version int64) ([]*goose.MigrationResult, error) {
	return m.provider.DownTo(ctx, version)
}

// Status lists every embedded migration and whether it is applied.
func (m *Migrator) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	return m.provider.Status(ctx)
}

// Versions returns the applied database version and the newest embedded one.
func (m *Migrator) Versions(ctx context.Context) (current, target int64, err error) {
	return m.provider.GetVersions(ctx)
}

// CheckSchema returns ErrSchemaBehind if migrations are pending. A database
// that is ahead of this binary is accepted (expand/contract deployments).
func (m *Migrator) CheckSchema(ctx context.Context) error {
	current, target, err := m.Versions(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current < target {
		return fmt.Errorf("%w: at %d, want %d", ErrSchemaBehind, current, target)
	}
	return nil
}

// Close releases the database/sql adapter. It does not close the pool.
func (m *Migrator) Close() error {
	return m.sqlDB.Close()
}

// Ready reports whether the database is reachable and fully migrated.
func Ready(ctx context.Context, pool *pgxpool.Pool, m *Migrator) error {
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return m.CheckSchema(ctx)
}
