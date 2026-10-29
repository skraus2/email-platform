// Package database owns the PostgreSQL connection pool, schema migrations and
// the schema-version check used for readiness.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/skraus2/email-platform/db"
)

// sqlstateUndefinedTable is PostgreSQL's undefined_table error code.
const sqlstateUndefinedTable = "42P01"

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
	pool     *pgxpool.Pool
	sqlDB    *sql.DB
	provider *goose.Provider
	target   int64 // newest embedded migration version
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
	var target int64
	for _, src := range provider.ListSources() {
		target = max(target, src.Version)
	}
	return &Migrator{pool: pool, sqlDB: sqlDB, provider: provider, target: target}, nil
}

// Up applies all pending migrations.
func (m *Migrator) Up(ctx context.Context) ([]*goose.MigrationResult, error) {
	res, err := m.provider.Up(ctx)
	if err != nil {
		return res, fmt.Errorf("migrate up: %w", err)
	}
	return res, nil
}

// DownTo rolls back to the given version (0 = everything). Used by tests that
// prove migrations are reversible; not exposed as a CLI command.
func (m *Migrator) DownTo(ctx context.Context, version int64) ([]*goose.MigrationResult, error) {
	res, err := m.provider.DownTo(ctx, version)
	if err != nil {
		return res, fmt.Errorf("migrate down to %d: %w", version, err)
	}
	return res, nil
}

// Status lists every embedded migration and whether it is applied.
func (m *Migrator) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	st, err := m.provider.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration status: %w", err)
	}
	return st, nil
}

// Versions returns the applied database version and the newest embedded one.
func (m *Migrator) Versions(ctx context.Context) (current, target int64, err error) {
	current, target, err = m.provider.GetVersions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("migration versions: %w", err)
	}
	return current, target, nil
}

// CheckSchema returns ErrSchemaBehind if migrations are pending. A database
// that is ahead of this binary is accepted (expand/contract deployments).
//
// It is a plain read on the pool on purpose: goose's GetVersions creates the
// version table and serialises on a mutex that ignores ctx, which a readiness
// probe must never do.
func (m *Migrator) CheckSchema(ctx context.Context) error {
	var current int64
	err := m.pool.QueryRow(ctx, "SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version").Scan(&current)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == sqlstateUndefinedTable:
		current = 0 // never migrated
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	}
	if current < m.target {
		return fmt.Errorf("%w: at %d, want %d", ErrSchemaBehind, current, m.target)
	}
	return nil
}

// Close releases the database/sql adapter. It does not close the pool.
func (m *Migrator) Close() error {
	if err := m.sqlDB.Close(); err != nil {
		return fmt.Errorf("close migration connection: %w", err)
	}
	return nil
}

// Ready reports whether the database is reachable and fully migrated.
func Ready(ctx context.Context, pool *pgxpool.Pool, m *Migrator) error {
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return m.CheckSchema(ctx)
}
