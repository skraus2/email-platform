package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// Queues lists every queue the worker may run.
func Queues() []string {
	return []string{river.QueueDefault, QueueBroadcasts, QueueAutomations, QueueWebhooks}
}

// UnfinishedStates lists the job states counted as backlog.
func UnfinishedStates() []string {
	return []string{"available", "pending", "retryable", "running", "scheduled"}
}

// QueueStats is a snapshot of the job queue for monitoring.
type QueueStats struct {
	// Jobs counts unfinished jobs by queue and state.
	Jobs map[[2]string]int64
	// OldestAvailable is, by queue and class (transactional or bulk), how
	// long the oldest job that is due has been waiting for a worker.
	OldestAvailable map[[2]string]float64
}

// Job classes: transactional sends run before bulk (broadcast and
// automation) ones.
const (
	ClassTransactional = "transactional"
	ClassBulk          = "bulk"
)

// Stats reads the queue snapshot from River's tables (no project data).
// This file holds only these two statements: they read queue names, states,
// priorities and times, never job args (which carry project and email ids).
func Stats(ctx context.Context, pool *pgxpool.Pool) (QueueStats, error) {
	s := QueueStats{Jobs: map[[2]string]int64{}, OldestAvailable: map[[2]string]float64{}}
	rows, err := pool.Query(ctx, `SELECT queue, state::text, count(*) FROM river_job
		WHERE state = ANY($1::text[]::river_job_state[]) GROUP BY 1, 2`, UnfinishedStates())
	if err != nil {
		return s, fmt.Errorf("count jobs: %w", err)
	}
	for rows.Next() {
		var queue, state string
		var n int64
		if err := rows.Scan(&queue, &state, &n); err != nil {
			rows.Close()
			return s, fmt.Errorf("count jobs: %w", err)
		}
		s.Jobs[[2]string{queue, state}] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, fmt.Errorf("count jobs: %w", err)
	}
	rows, err = pool.Query(ctx, `SELECT queue, priority = $1, extract(epoch FROM now() - min(scheduled_at))::float8 FROM river_job
		WHERE state = 'available' AND scheduled_at <= now() GROUP BY 1, 2`, priorityTransactional)
	if err != nil {
		return s, fmt.Errorf("queue lag: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var queue string
		var transactional bool
		var age float64
		if err := rows.Scan(&queue, &transactional, &age); err != nil {
			return s, fmt.Errorf("queue lag: %w", err)
		}
		class := ClassBulk
		if transactional {
			class = ClassTransactional
		}
		s.OldestAvailable[[2]string{queue, class}] = age
	}
	if err := rows.Err(); err != nil {
		return s, fmt.Errorf("queue lag: %w", err)
	}
	return s, nil
}
