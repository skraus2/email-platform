package webhooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/metrics"
	"github.com/skraus2/email-platform/internal/store"
)

// Outbox turns status changes into webhook deliveries.
type Outbox struct{ enq *jobs.Enqueuer }

// NewOutbox builds an Outbox that enqueues through enq.
func NewOutbox(enq *jobs.Enqueuer) *Outbox { return &Outbox{enq: enq} }

// OnStatusChange is an events.Hook: in the transaction that changed the
// status it creates one delivery (and its job) per active endpoint of the
// project subscribed to the event type.
func (o *Outbox) OnStatusChange(ctx context.Context, tx pgx.Tx, c events.Change) error {
	ids, err := store.New(tx).InsertWebhookDeliveries(ctx, store.InsertWebhookDeliveriesParams{
		EventID: c.EventID, ProjectID: c.ProjectID, EventType: "email." + c.Status,
	})
	if err != nil {
		return fmt.Errorf("insert webhook deliveries: %w", err)
	}
	args := make([]jobs.DeliverWebhookArgs, 0, len(ids))
	for _, id := range ids {
		args = append(args, jobs.DeliverWebhookArgs{DeliveryID: id, ProjectID: c.ProjectID})
	}
	return o.enq.EnqueueWebhooks(ctx, tx, args)
}

// DelivererConfig configures webhook delivery.
type DelivererConfig struct {
	Pool        *pgxpool.Pool
	Logger      *slog.Logger
	Keys        *Keyring
	Targets     TargetPolicy
	Timeout     time.Duration    // one HTTP request
	MaxAttempts int              // requests per delivery
	Retry       jobs.RetryPolicy // spacing of retries
}

// Deliverer sends webhook requests.
type Deliverer struct {
	cfg     DelivererConfig
	client  *http.Client
	metrics *metrics.Webhooks // nil: no metrics
}

// Observe sets where delivery results are counted. Set it before the
// deliverer is used.
func (d *Deliverer) Observe(m *metrics.Webhooks) { d.metrics = m }

// NewDeliverer checks the configuration and builds a Deliverer.
func NewDeliverer(cfg DelivererConfig) (*Deliverer, error) {
	switch {
	case cfg.Keys == nil:
		return nil, ErrNoKeys
	case cfg.Timeout <= 0 || cfg.MaxAttempts < 1:
		return nil, errors.New("webhooks: timeout and attempts must be positive")
	case cfg.Retry.Base <= 0 || cfg.Retry.Max < cfg.Retry.Base:
		return nil, errors.New("webhooks: retry policy needs 0 < Base <= Max")
	}
	return &Deliverer{cfg: cfg, client: cfg.Targets.Client(cfg.Timeout)}, nil
}

// Delivery failure codes, stored and returned to the queue. They never
// contain the URL, which may carry tokens.
const (
	CodeTimeout          = "timeout"
	CodeConnect          = "connect"
	CodeForbiddenTarget  = "forbidden_target"
	CodeEndpointDisabled = "endpoint_disabled"
	CodeSecret           = "secret_unavailable"
	CodeInternal         = "internal_error"
	CodeExpired          = "expired"
	CodeInvalidStatus    = "http_invalid"
)

func httpCode(status int) string { return "http_" + strconv.Itoa(status) }

// retryableStatus: the receiver may succeed later.
func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// maxResponseRead bounds how much of a response body is read (then dropped).
const maxResponseRead = 1 << 10

// Deliver is the deliver_webhook job handler. A planned retry is a snooze;
// an error means an internal failure the queue retries.
func (d *Deliverer) Deliver(ctx context.Context, args jobs.DeliverWebhookArgs, at jobs.Attempt) error {
	log := d.cfg.Logger.With(
		slog.String("project_id", args.ProjectID.String()),
		slog.String("delivery_id", args.DeliveryID.String()),
		slog.Int64("job_id", at.JobID),
	)
	q := store.New(d.cfg.Pool)
	row, err := q.LoadWebhookDelivery(ctx, store.LoadWebhookDeliveryParams{ID: args.DeliveryID, ProjectID: args.ProjectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // endpoint or email deleted since
	}
	if err != nil {
		return d.internal(ctx, log, args, at, fmt.Errorf("load delivery: %w", err))
	}
	if row.Status != "pending" {
		return nil
	}
	log = log.With(slog.String("endpoint_id", row.EndpointID.String()), slog.String("event_type", row.EventType))
	if row.EndpointStatus != "active" {
		return d.fail(ctx, log, args, CodeEndpointDisabled, nil, false)
	}
	target, err := d.cfg.Targets.ValidateURL(row.Url)
	if err != nil {
		return d.fail(ctx, log, args, CodeForbiddenTarget, nil, false)
	}
	log = log.With(slog.String("host", target.Hostname()))
	secret, err := secretOf(d.cfg.Keys, store.WebhookEndpoint{
		ID: row.EndpointID, ProjectID: row.ProjectID, SecretCiphertext: row.SecretCiphertext, SecretKeyID: row.SecretKeyID,
	})
	if err != nil {
		return d.internalCode(ctx, log, args, at, errors.New(CodeSecret), CodeSecret)
	}
	body, err := Payload(row.EventID, row.EventType, row.ReceivedAt, row.EmailID, row.ProjectID)
	if err != nil {
		return d.internal(ctx, log, args, at, fmt.Errorf("build payload: %w", err))
	}

	status, code := d.send(ctx, target, row.EventID, secret, body)
	wctx, cancel := detached(ctx)
	defer cancel()
	if status == 0 && errors.Is(ctx.Err(), context.Canceled) {
		// Shutdown interrupted the attempt: not counted, the queue retries.
		return errors.New(CodeInternal)
	}
	switch {
	case status >= 200 && status < 300:
		n, err := q.MarkDeliveryDelivered(wctx, store.MarkDeliveryDeliveredParams{
			ID: args.DeliveryID, ProjectID: args.ProjectID, LastStatusCode: int32ptr(status),
		})
		if err != nil {
			return d.internal(ctx, log, args, at, fmt.Errorf("mark delivered: %w", err))
		}
		d.metrics.Result(metrics.WebhookDelivered, int(n))
		log.InfoContext(ctx, "webhook delivered", slog.Int("status", status))
		return nil
	case code == CodeForbiddenTarget:
		return d.fail(ctx, log, args, code, nil, false)
	case status != 0 && (status < 100 || status > 599):
		// Not a real HTTP status: never retried, not stored as one.
		return d.fail(ctx, log, args, CodeInvalidStatus, nil, true)
	case status != 0 && !retryableStatus(status):
		return d.fail(ctx, log, args, code, int32ptr(status), true)
	}

	delay := d.cfg.Retry.Delay(int(row.Attempts)+1, rand.Float64)
	next := time.Now().Add(delay)
	var sc *int32
	if status != 0 {
		sc = int32ptr(status)
	}
	res, err := q.RecordDeliveryRetry(wctx, store.RecordDeliveryRetryParams{
		ID: args.DeliveryID, ProjectID: args.ProjectID, LastStatusCode: sc, LastErrorCode: &code, NextAttemptAt: next,
		MaxAttempts: int32(d.cfg.MaxAttempts), //nolint:gosec // bounded by config (≤ 25)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return d.internal(ctx, log, args, at, fmt.Errorf("record attempt: %w", err))
	}
	if res.Status == "failed" {
		log.WarnContext(ctx, "webhook delivery failed", slog.String("error_code", code), slog.Int("attempts", int(res.Attempts)))
		d.metrics.Result(metrics.WebhookFailed, 1)
		return nil
	}
	d.metrics.Result(metrics.WebhookRetry, 1)
	log.WarnContext(ctx, "webhook attempt failed; will retry", slog.String("error_code", code), slog.Int("attempts", int(res.Attempts)))
	return jobs.Snooze(delay)
}

// send makes one request. It returns the HTTP status (0 if none) and a
// failure code; neither contains the URL.
func (d *Deliverer) send(ctx context.Context, target *url.URL, eventID uuid.UUID, secret string, body []byte) (int, string) {
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return 0, CodeConnect
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "email-platform-webhooks/1")
	req.Header.Set(HeaderID, eventID.String())
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSignature, Sign(secret, ts, body))
	resp, err := d.client.Do(req)
	if err != nil {
		var ne net.Error
		switch {
		case errors.Is(err, ErrForbiddenTarget):
			return 0, CodeForbiddenTarget
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
			return 0, CodeTimeout
		default:
			return 0, CodeConnect
		}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseRead))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, ""
	}
	return resp.StatusCode, httpCode(resp.StatusCode)
}

// fail ends a delivery as failed. counted: this call ends an attempt that
// was not recorded yet.
func (d *Deliverer) fail(ctx context.Context, log *slog.Logger, args jobs.DeliverWebhookArgs, code string, status *int32, counted bool) error {
	wctx, cancel := detached(ctx)
	defer cancel()
	n, err := store.New(d.cfg.Pool).MarkDeliveryFailed(wctx, store.MarkDeliveryFailedParams{
		ID: args.DeliveryID, ProjectID: args.ProjectID, LastErrorCode: &code, Counted: counted, StatusCode: status,
	})
	if err != nil {
		log.ErrorContext(ctx, "mark webhook delivery failed", slog.Any("error", err))
		return errors.New(CodeInternal)
	}
	d.metrics.Result(metrics.WebhookFailed, int(n))
	log.WarnContext(ctx, "webhook delivery failed", slog.String("error_code", code))
	return nil
}

// internal handles errors outside the HTTP exchange: retried by the queue;
// on its last attempt the delivery fails, so it never stays pending.
func (d *Deliverer) internal(ctx context.Context, log *slog.Logger, args jobs.DeliverWebhookArgs, at jobs.Attempt, err error) error {
	return d.internalCode(ctx, log, args, at, err, CodeInternal)
}

func (d *Deliverer) internalCode(ctx context.Context, log *slog.Logger, args jobs.DeliverWebhookArgs, at jobs.Attempt, err error, code string) error {
	log.ErrorContext(ctx, "webhook delivery error", slog.Any("error", err))
	if at.Final() {
		return d.fail(ctx, log, args, code, nil, false)
	}
	return errors.New(CodeInternal)
}

// Cleanup fails deliveries still pending long after their last possible
// attempt (their job ended without a final state).
func (d *Deliverer) Cleanup(ctx context.Context) error {
	horizon := d.cfg.Retry.WorstCase(d.cfg.MaxAttempts) + time.Duration(d.cfg.MaxAttempts)*(d.cfg.Timeout+time.Minute) + time.Hour
	n, err := store.New(d.cfg.Pool).ExpireStaleDeliveries(ctx, time.Now().Add(-horizon))
	if err != nil {
		return fmt.Errorf("expire webhook deliveries: %w", err)
	}
	d.metrics.Result(metrics.WebhookFailed, int(n))
	if n > 0 {
		d.cfg.Logger.WarnContext(ctx, "webhook deliveries expired", slog.Int64("count", n))
	}
	return nil
}

// Delivery is a delivery as operators see it.
type Delivery struct {
	ID             uuid.UUID
	EndpointID     uuid.UUID
	EmailID        uuid.UUID
	EventType      string
	Status         string
	Attempts       int
	LastStatusCode *int32
	LastErrorCode  *string
	NextAttemptAt  *time.Time
	CreatedAt      time.Time
}

// ListDeliveries returns the project's deliveries, newest first, optionally
// with one status only.
func ListDeliveries(ctx context.Context, q *store.Queries, projectID uuid.UUID, status string, limit int) ([]Delivery, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	var filter *string
	switch status {
	case "":
	case "pending", "delivered", "failed":
		filter = &status
	default:
		return nil, errors.New("status must be pending, delivered or failed")
	}
	rows, err := q.ListWebhookDeliveries(ctx, store.ListWebhookDeliveriesParams{ProjectID: projectID, Status: filter, Limit: int32(limit)}) // bounded above
	if err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	out := make([]Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, Delivery{
			ID: r.ID, EndpointID: r.EndpointID, EmailID: r.EmailID, EventType: r.EventType, Status: r.Status,
			Attempts: int(r.Attempts), LastStatusCode: r.LastStatusCode, LastErrorCode: r.LastErrorCode,
			NextAttemptAt: r.NextAttemptAt, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

func int32ptr(v int) *int32 {
	n := int32(v) //nolint:gosec // HTTP status codes
	return &n
}

// detached lets a result be recorded although shutdown cancelled the job.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}
