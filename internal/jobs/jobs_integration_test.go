//go:build integration

package jobs_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/testdb"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func countJobs(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'send_email'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEnqueueIsTransactional(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	enq, err := jobs.NewEnqueuer(pool, quiet)
	if err != nil {
		t.Fatal(err)
	}
	args := jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := enq.EnqueueSendEmail(ctx, tx, args); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, pool); n != 0 {
		t.Fatalf("jobs after rollback = %d, want 0", n)
	}

	tx, _ = pool.Begin(ctx)
	if err := enq.EnqueueSendEmail(ctx, tx, args); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, pool); n != 1 {
		t.Fatalf("jobs after commit = %d, want 1", n)
	}
	var maxAttempts int
	if err := pool.QueryRow(ctx, "SELECT max_attempts FROM river_job WHERE kind = 'send_email'").Scan(&maxAttempts); err != nil {
		t.Fatal(err)
	}
	if maxAttempts != jobs.SendEmailMaxAttempts {
		t.Fatalf("max_attempts = %d, want %d", maxAttempts, jobs.SendEmailMaxAttempts)
	}
}

type recorder struct {
	mu       sync.Mutex
	attempts []jobs.Attempt
	args     []jobs.SendEmailArgs
	done     chan struct{}
	cleaned  chan struct{}
}

func newRecorder() *recorder {
	return &recorder{done: make(chan struct{}, 10), cleaned: make(chan struct{}, 10)}
}

func (r *recorder) handlers() jobs.Handlers {
	return jobs.Handlers{
		SendEmail: func(_ context.Context, a jobs.SendEmailArgs, at jobs.Attempt) error {
			r.mu.Lock()
			r.attempts = append(r.attempts, at)
			r.args = append(r.args, a)
			r.mu.Unlock()
			r.done <- struct{}{}
			return nil
		},
		Cleanup: func(context.Context) error {
			r.cleaned <- struct{}{}
			return nil
		},
	}
}

func startWorker(t *testing.T, pool *pgxpool.Pool, h jobs.Handlers, rescueAfter time.Duration) {
	t.Helper()
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: quiet, Handlers: h, Concurrency: 2,
		JobTimeout: 500 * time.Millisecond, RescueStuckJobsAfter: rescueAfter,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Stop(5 * time.Second); err != nil {
			t.Errorf("stop worker: %v", err)
		}
	})
}

func waitDone(t *testing.T, r *recorder, timeout time.Duration) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(timeout):
		t.Fatalf("handler not called within %v", timeout)
	}
}

func TestWorkerRunsSendEmailAndCleanup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	rec := newRecorder()
	startWorker(t, pool, rec.handlers(), 0)

	enq, _ := jobs.NewEnqueuer(pool, quiet)
	args := jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}
	tx, _ := pool.Begin(ctx)
	if err := enq.EnqueueSendEmail(ctx, tx, args); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	waitDone(t, rec, 15*time.Second)

	rec.mu.Lock()
	gotArgs, gotAttempt := rec.args[0], rec.attempts[0]
	rec.mu.Unlock()
	if gotArgs != args {
		t.Errorf("handler args = %+v, want %+v", gotArgs, args)
	}
	if a := gotAttempt; a.Number != 1 || a.MaxAttempts != jobs.SendEmailMaxAttempts || a.Final() {
		t.Errorf("attempt = %+v", a)
	}
	select {
	case <-rec.cleaned:
	case <-time.After(15 * time.Second):
		t.Error("cleanup did not run on start (after leader election)")
	}
}

// A worker that dies mid-job leaves the job "running". The rescuer must make
// it available again so the email is not lost (it may be sent twice; the
// handler detects and logs that case).
func TestStuckJobIsRescuedAndRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	enq, _ := jobs.NewEnqueuer(pool, quiet)
	args := jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}
	tx, _ := pool.Begin(ctx)
	if err := enq.EnqueueSendEmail(ctx, tx, args); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed worker: running, attempted long ago, attempt 1.
	if _, err := pool.Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1,
		attempted_at = now() - interval '1 hour', attempted_by = '{crashed-worker}'
		WHERE kind = 'send_email'`); err != nil {
		t.Fatal(err)
	}

	rec := newRecorder()
	startWorker(t, pool, rec.handlers(), time.Second)
	waitDone(t, rec, 30*time.Second)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if a := rec.attempts[0]; a.Number != 2 {
		t.Errorf("rescued job ran as attempt %d, want 2", a.Number)
	}
}
