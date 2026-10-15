package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Unprepared runs every query as an unnamed statement, so PostgreSQL plans it
// with the actual parameter values each time. Use it for queries whose best
// plan depends on those values (optional filters, cursors), where a cached
// generic plan could scan far more rows than needed.
func Unprepared(db DBTX) DBTX { return unprepared{db} }

type unprepared struct{ db DBTX }

func withMode(args []any) []any { return append([]any{pgx.QueryExecModeDescribeExec}, args...) }

func (u unprepared) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return u.db.Exec(ctx, sql, withMode(args)...)
}

func (u unprepared) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return u.db.Query(ctx, sql, withMode(args)...)
}

func (u unprepared) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return u.db.QueryRow(ctx, sql, withMode(args)...)
}

// ReadOnlyWithTimeout runs fn in a read-only transaction whose statements
// the server cancels after timeout (SQLSTATE 57014). A client-side context
// deadline alone only drops the connection; the server would keep running
// an expensive query to the end. Queries run as unnamed statements.
func ReadOnlyWithTimeout(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration, fn func(q *Queries) error) error {
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", max(timeout.Milliseconds(), 1))); err != nil {
			return fmt.Errorf("set statement timeout: %w", err)
		}
		return fn(New(Unprepared(tx)))
	})
}

// StatementTimedOut reports whether err is a server-side statement timeout.
func StatementTimedOut(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "57014"
}
