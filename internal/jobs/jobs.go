// Package jobs is the only place that knows the job queue implementation
// (River). Other packages enqueue work inside their own transaction and
// register plain handler functions; they never import River.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// SendEmailArgs identifies the email to dispatch. Both ids are carried so the
// handler can scope every query by project.
type SendEmailArgs struct {
	EmailID   uuid.UUID `json:"email_id"`
	ProjectID uuid.UUID `json:"project_id"`
}

// Kind implements river.JobArgs.
func (SendEmailArgs) Kind() string { return "send_email" }

type cleanupArgs struct{}

func (cleanupArgs) Kind() string { return "cleanup_emails" }

// Snooze, returned by a handler, runs the job again after d (at least
// MinSnooze) without using up an attempt (backpressure, not failure).
func Snooze(d time.Duration) error { return &snoozeError{d: d} }

// MinSnooze keeps a snoozing job from spinning: snoozes cost no attempt, so
// a near-zero snooze would loop on the database without bound.
const MinSnooze = 100 * time.Millisecond

func snoozeFor(d time.Duration) time.Duration { return max(d, MinSnooze) }

type snoozeError struct{ d time.Duration }

func (e *snoozeError) Error() string { return "snoozed for " + e.d.String() }

// Attempt describes the current execution of a job.
type Attempt struct {
	JobID       int64
	Number      int // 1-based
	MaxAttempts int
}

// Final reports whether this is the last attempt River will make.
func (a Attempt) Final() bool { return a.Number >= a.MaxAttempts }

// Handlers are the functions the worker runs.
type Handlers struct {
	SendEmail func(ctx context.Context, args SendEmailArgs, a Attempt) error
	// Cleanup runs periodically (and once on start).
	Cleanup func(ctx context.Context) error
}

// Enqueuer inserts jobs. The API process uses it insert-only.
type Enqueuer struct {
	client      *river.Client[pgx.Tx]
	maxAttempts int
}

// NewEnqueuer builds an insert-only client (it never works jobs). Every
// send_email job gets sendMaxAttempts attempts.
func NewEnqueuer(pool *pgxpool.Pool, logger *slog.Logger, sendMaxAttempts int) (*Enqueuer, error) {
	if sendMaxAttempts < 1 {
		return nil, errors.New("jobs: sendMaxAttempts must be at least 1")
	}
	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("create job client: %w", err)
	}
	return &Enqueuer{client: c, maxAttempts: sendMaxAttempts}, nil
}

// EnqueueSendEmail inserts the dispatch job in tx. The job only becomes
// visible to workers when tx commits, and disappears if it rolls back.
func (e *Enqueuer) EnqueueSendEmail(ctx context.Context, tx pgx.Tx, args SendEmailArgs) error {
	if _, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{MaxAttempts: e.maxAttempts}); err != nil {
		return fmt.Errorf("enqueue send_email: %w", err)
	}
	return nil
}

// WorkerConfig configures the job-processing side.
type WorkerConfig struct {
	Pool            *pgxpool.Pool
	Logger          *slog.Logger
	Handlers        Handlers
	Retry           RetryPolicy // spacing of send_email retries
	Concurrency     int
	JobTimeout      time.Duration
	CleanupInterval time.Duration
	// RescueStuckJobsAfter: a job still "running" this long after its attempt
	// started (e.g. the worker crashed) is made available again. Zero = River
	// default.
	RescueStuckJobsAfter time.Duration
}

// Worker runs job handlers until stopped.
type Worker struct {
	client *river.Client[pgx.Tx]
}

// NewWorker registers the handlers and builds the client.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.Handlers.SendEmail == nil || cfg.Handlers.Cleanup == nil {
		return nil, errors.New("jobs: all handlers are required")
	}
	if cfg.Retry.Base <= 0 || cfg.Retry.Max < cfg.Retry.Base {
		return nil, errors.New("jobs: retry policy needs 0 < Base <= Max")
	}
	workers := river.NewWorkers()
	river.AddWorker(workers, &sendEmailWorker{fn: cfg.Handlers.SendEmail, retry: cfg.Retry})
	river.AddWorker(workers, &cleanupWorker{fn: cfg.Handlers.Cleanup})

	interval := cfg.CleanupInterval
	if interval <= 0 {
		interval = time.Hour
	}
	c, err := river.NewClient(riverpgxv5.New(cfg.Pool), &river.Config{
		Logger:               cfg.Logger,
		Workers:              workers,
		Queues:               map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: max(cfg.Concurrency, 1)}},
		JobTimeout:           cfg.JobTimeout,
		RescueStuckJobsAfter: cfg.RescueStuckJobsAfter,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(interval),
				func() (river.JobArgs, *river.InsertOpts) { return cleanupArgs{}, nil },
				&river.PeriodicJobOpts{ID: "cleanup_emails", RunOnStart: true}),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create job worker: %w", err)
	}
	return &Worker{client: c}, nil
}

// Start begins fetching and working jobs. Cancelling ctx does NOT stop the
// worker: River would cancel running jobs mid-send on context cancellation.
// Shutdown always goes through Stop, which lets in-flight jobs finish.
func (w *Worker) Start(ctx context.Context) error {
	return w.client.Start(context.WithoutCancel(ctx))
}

// Stop waits for running jobs up to timeout, then cancels the rest. A job
// cancelled mid-send may be retried later; that duplicate risk is inherent to
// external side effects and is logged by the handler on the next attempt.
func (w *Worker) Stop(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := w.client.Stop(ctx); err == nil {
		return nil
	}
	hard, cancelHard := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHard()
	return w.client.StopAndCancel(hard)
}

type sendEmailWorker struct {
	river.WorkerDefaults[SendEmailArgs]
	fn    func(context.Context, SendEmailArgs, Attempt) error
	retry RetryPolicy
}

// NextRetry replaces River's default schedule. Like River's own policy it
// numbers retries by recorded errors (the current one is not in the row
// yet); snoozes record no error and so do not advance the backoff.
func (w *sendEmailWorker) NextRetry(job *river.Job[SendEmailArgs]) time.Time {
	return time.Now().Add(w.retry.Delay(len(job.Errors)+1, rand.Float64))
}

func (w *sendEmailWorker) Work(ctx context.Context, job *river.Job[SendEmailArgs]) error {
	err := w.fn(ctx, job.Args, Attempt{JobID: job.ID, Number: job.Attempt, MaxAttempts: job.MaxAttempts})
	var snooze *snoozeError
	if errors.As(err, &snooze) {
		return river.JobSnooze(snoozeFor(snooze.d))
	}
	return err
}

type cleanupWorker struct {
	river.WorkerDefaults[cleanupArgs]
	fn func(context.Context) error
}

func (w *cleanupWorker) Work(ctx context.Context, _ *river.Job[cleanupArgs]) error { return w.fn(ctx) }

// Timeout gives cleanup its own budget: it can touch many rows and must not
// inherit the short per-email job timeout.
func (w *cleanupWorker) Timeout(*river.Job[cleanupArgs]) time.Duration { return 5 * time.Minute }
