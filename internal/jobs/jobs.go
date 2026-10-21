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

// DeliverWebhookArgs identifies one webhook delivery.
type DeliverWebhookArgs struct {
	DeliveryID uuid.UUID `json:"delivery_id"`
	ProjectID  uuid.UUID `json:"project_id"`
}

// Kind implements river.JobArgs.
func (DeliverWebhookArgs) Kind() string { return "deliver_webhook" }

// BroadcastFanoutArgs identifies a broadcast whose recipient emails are to
// be created.
type BroadcastFanoutArgs struct {
	BroadcastID uuid.UUID `json:"broadcast_id"`
	ProjectID   uuid.UUID `json:"project_id"`
}

// Kind implements river.JobArgs.
func (BroadcastFanoutArgs) Kind() string { return "broadcast_fanout" }

// QueueBroadcasts runs broadcast fan-out apart from email dispatch.
const QueueBroadcasts = "broadcasts"

// broadcastFanoutAttempts bounds internal failures of one fan-out; each run
// does one batch and continues by snoozing, which costs no attempt.
const broadcastFanoutAttempts = 25

// QueueWebhooks runs webhook deliveries apart from email dispatch, so slow
// receivers can never take the slots sends need.
const QueueWebhooks = "webhooks"

// webhookJobAttempts bounds how often a delivery job may fail internally
// (database errors). Planned retries are snoozes and count in the delivery
// row, not here.
const webhookJobAttempts = 25

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

// SnoozeDelay reports whether err is a snooze and for how long.
func SnoozeDelay(err error) (time.Duration, bool) {
	var s *snoozeError
	if errors.As(err, &s) {
		return s.d, true
	}
	return 0, false
}

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
	// DeliverWebhook delivers one webhook notification (nil: the process
	// runs no webhook queue).
	DeliverWebhook func(ctx context.Context, args DeliverWebhookArgs, a Attempt) error
	// Cleanup runs periodically (and once on start).
	Cleanup func(ctx context.Context) error
	// BroadcastFanout creates the recipient emails of one broadcast batch
	// (nil: the process runs no broadcast queue).
	BroadcastFanout func(ctx context.Context, args BroadcastFanoutArgs, a Attempt) error
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

// SendOpts adjust a send_email job.
type SendOpts struct {
	// ScheduledAt: the job runs not before this time (zero: now).
	ScheduledAt time.Time
	// Bulk marks a broadcast send: it is fetched after every available
	// transactional send of the queue.
	Bulk bool
}

// Job priorities within a queue: lower numbers are fetched first.
const (
	priorityTransactional = 1
	priorityBulk          = 2
)

// EnqueueSendEmail inserts the dispatch job in tx. The job only becomes
// visible to workers when tx commits, and disappears if it rolls back.
func (e *Enqueuer) EnqueueSendEmail(ctx context.Context, tx pgx.Tx, args SendEmailArgs, opts SendOpts) error {
	if _, err := e.client.InsertTx(ctx, tx, args, e.sendOpts(opts)); err != nil {
		return fmt.Errorf("enqueue send_email: %w", err)
	}
	return nil
}

// EnqueueSendEmails inserts one dispatch job per email in tx, all with the
// same options.
func (e *Enqueuer) EnqueueSendEmails(ctx context.Context, tx pgx.Tx, args []SendEmailArgs, opts SendOpts) error {
	if len(args) == 0 {
		return nil
	}
	params := make([]river.InsertManyParams, 0, len(args))
	for _, a := range args {
		params = append(params, river.InsertManyParams{Args: a, InsertOpts: e.sendOpts(opts)})
	}
	if _, err := e.client.InsertManyTx(ctx, tx, params); err != nil {
		return fmt.Errorf("enqueue send_email: %w", err)
	}
	return nil
}

func (e *Enqueuer) sendOpts(opts SendOpts) *river.InsertOpts {
	o := &river.InsertOpts{MaxAttempts: e.maxAttempts, ScheduledAt: opts.ScheduledAt, Priority: priorityTransactional}
	if opts.Bulk {
		o.Priority = priorityBulk
	}
	return o
}

// EnqueueBroadcastFanout inserts the fan-out job of a broadcast in tx, to run
// not before at.
func (e *Enqueuer) EnqueueBroadcastFanout(ctx context.Context, tx pgx.Tx, args BroadcastFanoutArgs, at time.Time) error {
	if _, err := e.client.InsertTx(ctx, tx, args, &river.InsertOpts{
		Queue: QueueBroadcasts, MaxAttempts: broadcastFanoutAttempts, ScheduledAt: at,
	}); err != nil {
		return fmt.Errorf("enqueue broadcast_fanout: %w", err)
	}
	return nil
}

// EnqueueWebhooks inserts delivery jobs in tx, in the webhook queue.
func (e *Enqueuer) EnqueueWebhooks(ctx context.Context, tx pgx.Tx, args []DeliverWebhookArgs) error {
	if len(args) == 0 {
		return nil
	}
	params := make([]river.InsertManyParams, 0, len(args))
	for _, a := range args {
		params = append(params, river.InsertManyParams{Args: a, InsertOpts: &river.InsertOpts{Queue: QueueWebhooks, MaxAttempts: webhookJobAttempts}})
	}
	if _, err := e.client.InsertManyTx(ctx, tx, params); err != nil {
		return fmt.Errorf("enqueue deliver_webhook: %w", err)
	}
	return nil
}

// WorkerConfig configures the job-processing side.
type WorkerConfig struct {
	Pool        *pgxpool.Pool
	Logger      *slog.Logger
	Handlers    Handlers
	Retry       RetryPolicy // spacing of send_email retries
	Concurrency int
	JobTimeout  time.Duration
	// WebhookConcurrency and WebhookTimeout size the webhook queue; a
	// delivery job may run WebhookTimeout plus a database margin.
	WebhookConcurrency int
	WebhookTimeout     time.Duration
	CleanupInterval    time.Duration
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
	queues := map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: max(cfg.Concurrency, 1)}}
	if cfg.Handlers.BroadcastFanout != nil {
		river.AddWorker(workers, &broadcastFanoutWorker{fn: cfg.Handlers.BroadcastFanout, retry: cfg.Retry})
		// One fan-out at a time per process: batches are database-heavy and
		// each broadcast is processed in order anyway.
		queues[QueueBroadcasts] = river.QueueConfig{MaxWorkers: 1}
	}
	if cfg.Handlers.DeliverWebhook != nil {
		river.AddWorker(workers, &deliverWebhookWorker{
			fn: cfg.Handlers.DeliverWebhook, retry: cfg.Retry, timeout: max(cfg.WebhookTimeout, time.Second) + webhookDBMargin,
		})
		queues[QueueWebhooks] = river.QueueConfig{MaxWorkers: max(cfg.WebhookConcurrency, 1)}
	}

	interval := cfg.CleanupInterval
	if interval <= 0 {
		interval = time.Hour
	}
	c, err := river.NewClient(riverpgxv5.New(cfg.Pool), &river.Config{
		Logger:               cfg.Logger,
		Workers:              workers,
		Queues:               queues,
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

type broadcastFanoutWorker struct {
	river.WorkerDefaults[BroadcastFanoutArgs]
	fn    func(context.Context, BroadcastFanoutArgs, Attempt) error
	retry RetryPolicy
}

// NextRetry spaces retries after internal errors (continuation snoozes).
func (w *broadcastFanoutWorker) NextRetry(job *river.Job[BroadcastFanoutArgs]) time.Time {
	return time.Now().Add(w.retry.Delay(len(job.Errors)+1, rand.Float64))
}

func (w *broadcastFanoutWorker) Work(ctx context.Context, job *river.Job[BroadcastFanoutArgs]) error {
	err := w.fn(ctx, job.Args, Attempt{JobID: job.ID, Number: job.Attempt, MaxAttempts: job.MaxAttempts})
	var snooze *snoozeError
	if errors.As(err, &snooze) {
		return river.JobSnooze(snoozeFor(snooze.d))
	}
	return err
}

// webhookDBMargin is the time a delivery job may spend on database work
// besides the HTTP request.
const webhookDBMargin = 10 * time.Second

type deliverWebhookWorker struct {
	river.WorkerDefaults[DeliverWebhookArgs]
	fn      func(context.Context, DeliverWebhookArgs, Attempt) error
	retry   RetryPolicy
	timeout time.Duration
}

// NextRetry spaces retries after internal errors (planned retries snooze).
func (w *deliverWebhookWorker) NextRetry(job *river.Job[DeliverWebhookArgs]) time.Time {
	return time.Now().Add(w.retry.Delay(len(job.Errors)+1, rand.Float64))
}

// Timeout bounds one delivery attempt independently of email dispatch.
func (w *deliverWebhookWorker) Timeout(*river.Job[DeliverWebhookArgs]) time.Duration {
	return w.timeout
}

func (w *deliverWebhookWorker) Work(ctx context.Context, job *river.Job[DeliverWebhookArgs]) error {
	err := w.fn(ctx, job.Args, Attempt{JobID: job.ID, Number: job.Attempt, MaxAttempts: job.MaxAttempts})
	var snooze *snoozeError
	if errors.As(err, &snooze) {
		return river.JobSnooze(snoozeFor(snooze.d))
	}
	return err
}
