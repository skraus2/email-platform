//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/testdb"
)

var quiet = slog.New(slog.DiscardHandler)

const testMaxAttempts = 5

var testRetry = jobs.RetryPolicy{Base: time.Second, Max: 10 * time.Second}

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
	enq, err := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	args := jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := enq.EnqueueSendEmail(ctx, tx, args, jobs.SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(t, pool); n != 0 {
		t.Fatalf("jobs after rollback = %d, want 0", n)
	}

	tx, _ = pool.Begin(ctx)
	if err := enq.EnqueueSendEmail(ctx, tx, args, jobs.SendOpts{}); err != nil {
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
	if maxAttempts != testMaxAttempts {
		t.Fatalf("max_attempts = %d, want %d", maxAttempts, testMaxAttempts)
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
		Pool: pool, Logger: quiet, Handlers: h, Concurrency: 2, Retry: testRetry,
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

	enq, _ := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	args := jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}
	tx, _ := pool.Begin(ctx)
	if err := enq.EnqueueSendEmail(ctx, tx, args, jobs.SendOpts{}); err != nil {
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
	if a := gotAttempt; a.Number != 1 || a.MaxAttempts != testMaxAttempts || a.Final() {
		t.Errorf("attempt = %+v", a)
	}
	select {
	case <-rec.cleaned:
	case <-time.After(15 * time.Second):
		t.Error("cleanup did not run on start (after leader election)")
	}
}

// The usage rollup runs on start as its own periodic job with a single
// attempt: a failing run is not retried by the queue.
func TestUsageRollupPeriodic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	ran := make(chan struct{}, 10)
	h := newRecorder().handlers()
	h.UsageRollup = func(context.Context) error {
		ran <- struct{}{}
		return errors.New("rollup failed")
	}
	startWorker(t, pool, h, 0)
	select {
	case <-ran:
	case <-time.After(15 * time.Second):
		t.Fatal("usage rollup did not run on start (after leader election)")
	}
	var state string
	var attempts, maxAttempts int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err := pool.QueryRow(ctx, `SELECT state, attempt, max_attempts FROM river_job WHERE kind = 'usage_rollup'`).Scan(&state, &attempts, &maxAttempts)
		if err == nil && state == "discarded" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if state != "discarded" || attempts != 1 || maxAttempts != 1 {
		t.Errorf("rollup job state=%s attempt=%d max_attempts=%d, want discarded after its single attempt", state, attempts, maxAttempts)
	}
	select {
	case <-ran:
		t.Error("failed rollup was retried by the queue")
	case <-time.After(2 * time.Second):
	}
}

// A worker that dies mid-job leaves the job "running". The rescuer must make
// it available again so the email is not lost (it may be sent twice; the
// handler detects and logs that case).
func TestStuckJobIsRescuedAndRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	enq, _ := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	args := jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}
	tx, _ := pool.Begin(ctx)
	if err := enq.EnqueueSendEmail(ctx, tx, args, jobs.SendOpts{}); err != nil {
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

// Cancelling the context passed to Start (the process signal context) must
// not cancel running jobs: shutdown goes through Stop, which lets in-flight
// sends finish within the shutdown budget.
func TestStartContextCancelDoesNotCancelRunningJobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	running := make(chan struct{})
	result := make(chan error, 1)
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: quiet, Concurrency: 1, JobTimeout: 10 * time.Second, Retry: testRetry,
		Handlers: jobs.Handlers{
			SendEmail: func(jobCtx context.Context, _ jobs.SendEmailArgs, _ jobs.Attempt) error {
				close(running)
				select {
				case <-time.After(500 * time.Millisecond):
					result <- nil
				case <-jobCtx.Done():
					result <- jobCtx.Err()
				}
				return nil
			},
			Cleanup: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	startCtx, cancelStart := context.WithCancel(ctx)
	if err := w.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	enq, _ := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	tx, _ := pool.Begin(ctx)
	_ = enq.EnqueueSendEmail(ctx, tx, jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}, jobs.SendOpts{})
	_ = tx.Commit(ctx)

	select {
	case <-running:
	case <-time.After(15 * time.Second):
		t.Fatal("job never started")
	}
	cancelStart() // SIGTERM arrives while the job runs
	if err := w.Stop(5 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("running job was cancelled by the start context: %v", err)
	}
}

// A failed send_email job is rescheduled by the configured policy, not by
// River's default (which would retry the first time after about 1 s).
func TestFailedSendIsRescheduledByPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	failed := make(chan struct{}, 1)
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: quiet, Concurrency: 1, JobTimeout: 10 * time.Second,
		Retry: jobs.RetryPolicy{Base: time.Minute, Max: time.Hour},
		Handlers: jobs.Handlers{
			SendEmail: func(context.Context, jobs.SendEmailArgs, jobs.Attempt) error {
				failed <- struct{}{}
				return errors.New("delivery failed: connect")
			},
			Cleanup: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })
	enq, _ := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	tx, _ := pool.Begin(ctx)
	if err := enq.EnqueueSendEmail(ctx, tx, jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}, jobs.SendOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed:
	case <-time.After(15 * time.Second):
		t.Fatal("job never ran")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var state string
		var wait float64
		err := pool.QueryRow(ctx, `SELECT state, extract(epoch FROM scheduled_at - (errors[1]->>'at')::timestamptz)
			FROM river_job WHERE kind = 'send_email'`).Scan(&state, &wait)
		if err == nil && state == "retryable" {
			// First retry: between Base/2 and Base (1 s slack for clocks).
			if wait < 29 || wait > 61 {
				t.Fatalf("first retry scheduled %.1fs after the error, want 30–60s", wait)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job not retryable: state %q, err %v", state, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEnqueueRequiresAttempts(t *testing.T) {
	t.Parallel()
	if _, err := jobs.NewEnqueuer(testdb.NewMigrated(t), quiet, 0); err == nil {
		t.Fatal("zero attempts accepted")
	}
}

// Webhook deliveries run in their own queue with their own timeout; a
// snooze reruns the job without using an attempt or recording an error.
func TestWorkerRunsWebhookQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	var mu sync.Mutex
	var attempts []jobs.Attempt
	var remaining []time.Duration
	done := make(chan struct{})
	h := newRecorder().handlers()
	h.DeliverWebhook = func(ctx context.Context, _ jobs.DeliverWebhookArgs, a jobs.Attempt) error {
		deadline, _ := ctx.Deadline()
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, a)
		remaining = append(remaining, time.Until(deadline))
		if len(attempts) == 1 {
			return jobs.Snooze(100 * time.Millisecond)
		}
		close(done)
		return nil
	}
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: quiet, Handlers: h, Concurrency: 1, Retry: testRetry,
		JobTimeout: 500 * time.Millisecond, WebhookConcurrency: 1, WebhookTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })

	enq, _ := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	tx, _ := pool.Begin(ctx)
	if err := enq.EnqueueWebhooks(ctx, tx, []jobs.DeliverWebhookArgs{{DeliveryID: uuid.New(), ProjectID: uuid.New()}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("webhook job not run twice")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts[0].Number != 1 || attempts[1].Number != 1 || attempts[1].Final() {
		t.Fatalf("attempts = %+v, want the snooze not to use an attempt", attempts)
	}
	// 2 s request budget + 10 s database margin, not the 500 ms send budget.
	for _, r := range remaining {
		if r < 11*time.Second || r > 12*time.Second {
			t.Fatalf("job deadline in %v, want ≈ 12 s", r)
		}
	}
	var queue string
	var errs int
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := pool.QueryRow(ctx, "SELECT queue, coalesce(array_length(errors, 1), 0) FROM river_job WHERE kind = 'deliver_webhook' AND state = 'completed'").Scan(&queue, &errs); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("webhook job not completed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if queue != jobs.QueueWebhooks || errs != 0 {
		t.Fatalf("queue %q, %d errors; want webhooks and none", queue, errs)
	}
}

// A transactional send is fetched before a backlog of broadcast sends that
// was queued earlier.
func TestBulkSendsYieldToTransactionalSends(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	enq, err := jobs.NewEnqueuer(pool, quiet, 5)
	if err != nil {
		t.Fatal(err)
	}
	var bulk []jobs.SendEmailArgs
	for range 20 {
		bulk = append(bulk, jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()})
	}
	transactional := jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: uuid.New()}
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := enq.EnqueueSendEmails(ctx, tx, bulk, jobs.SendOpts{Bulk: true}); err != nil {
			return err
		}
		return enq.EnqueueSendEmail(ctx, tx, transactional, jobs.SendOpts{})
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []uuid.UUID
	done := make(chan struct{})
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: quiet, Concurrency: 1, JobTimeout: 10 * time.Second, Retry: testRetry,
		Handlers: jobs.Handlers{
			SendEmail: func(_ context.Context, a jobs.SendEmailArgs, _ jobs.Attempt) error {
				mu.Lock()
				defer mu.Unlock()
				order = append(order, a.EmailID)
				if len(order) == 21 {
					close(done)
				}
				return nil
			},
			Cleanup: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Stop(5 * time.Second) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("jobs did not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if order[0] != transactional.EmailID {
		t.Fatalf("transactional send ran at position %d of 21", slices.Index(order, transactional.EmailID)+1)
	}
}

func TestWorkerRunsBroadcastQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	var mu sync.Mutex
	var attempts []jobs.Attempt
	var remaining []time.Duration
	done := make(chan struct{})
	h := newRecorder().handlers()
	h.BroadcastFanout = func(ctx context.Context, _ jobs.BroadcastFanoutArgs, a jobs.Attempt) error {
		deadline, _ := ctx.Deadline()
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, a)
		remaining = append(remaining, time.Until(deadline))
		if len(attempts) < 3 {
			return jobs.Snooze(0) // the next batch
		}
		close(done)
		return nil
	}
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: quiet, Handlers: h, Concurrency: 1, Retry: testRetry, JobTimeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })

	enq, _ := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	tx, _ := pool.Begin(ctx)
	if err := enq.EnqueueBroadcastFanout(ctx, tx, jobs.BroadcastFanoutArgs{BroadcastID: uuid.New(), ProjectID: uuid.New()}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("fan-out job not run three times")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range attempts {
		if a.Number != 1 {
			t.Fatalf("attempts = %+v, want snoozes not to use an attempt", attempts)
		}
	}
	// Its own 2 min budget, not the 500 ms send budget.
	for _, r := range remaining {
		if r < 110*time.Second || r > 121*time.Second {
			t.Fatalf("job deadline in %v, want ≈ 2 min", r)
		}
	}
	var queue, state string
	var errs int
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := pool.QueryRow(ctx, "SELECT queue, state, coalesce(array_length(errors, 1), 0) FROM river_job WHERE kind = 'broadcast_fanout'").Scan(&queue, &state, &errs); err != nil {
			t.Fatal(err)
		}
		if state == "completed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if queue != jobs.QueueBroadcasts || state != "completed" || errs != 0 {
		t.Fatalf("job queue=%s state=%s errors=%d", queue, state, errs)
	}
}
