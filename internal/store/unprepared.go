package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
