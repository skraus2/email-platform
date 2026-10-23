// Package usage keeps the daily usage of every project: how many emails were
// accepted, handed to the provider, failed, cancelled, delivered, bounced
// (permanently) and complained about on each UTC day. The worker recomputes
// the most recent days from emails and their events; older days keep their
// stored counts after retention deletes the underlying rows.
package usage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

const (
	day = 24 * time.Hour
	// RecentDays is how many UTC days (today included) every run
	// recomputes: late events of the previous days are still counted.
	RecentDays = 3
	// MaxCatchUpDays bounds how far back a run reaches after the rollup has
	// not run for a while. It stays below the shortest event retention
	// (15 days), so a recomputed day still has all of its events.
	MaxCatchUpDays = 14
)

// Result describes one rollup run.
type Result struct {
	From time.Time // start of the first recomputed UTC day
	Rows int64     // usage rows written
}

// Rollup recomputes the usage of every project from the earlier of the
// recent days and the day of the last run (at most MaxCatchUpDays back)
// through the day of now. Concurrent runs are safe: counts only grow.
func Rollup(ctx context.Context, pool *pgxpool.Pool, now time.Time) (Result, error) {
	q := store.New(pool)
	last, err := q.LastUsageRollup(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("last usage rollup: %w", err)
	}
	today := now.UTC().Truncate(day)
	from := today.Add(-(RecentDays - 1) * day)
	if lastDay := last.UTC().Truncate(day); lastDay.Before(from) {
		from = lastDay
	}
	if limit := today.Add(-(MaxCatchUpDays - 1) * day); from.Before(limit) {
		from = limit
	}
	rows, err := q.RollupUsage(ctx, store.RollupUsageParams{FromTime: from, UntilTime: today.Add(day)})
	if err != nil {
		return Result{}, fmt.Errorf("roll up usage: %w", err)
	}
	return Result{From: from, Rows: rows}, nil
}

// Roller runs the rollup as the worker's periodic job.
type Roller struct {
	Pool   *pgxpool.Pool
	Logger *slog.Logger
}

// Run rolls up usage as of now and logs the run.
func (r Roller) Run(ctx context.Context) error {
	start := time.Now()
	res, err := Rollup(ctx, r.Pool, start)
	if err != nil {
		return err
	}
	r.Logger.InfoContext(ctx, "usage rolled up", slog.Time("from", res.From), slog.Int64("rows", res.Rows),
		slog.Duration("duration", time.Since(start)))
	return nil
}
