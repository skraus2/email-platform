package emails

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
)

// Dispatcher sends queued emails through the provider. It runs in the worker.
//
// Exactly-once delivery is impossible with an external provider: if the
// provider accepted a message but the worker died before recording it, the
// retry sends it again. Such attempts are detectable (the email is already
// "sending" when an attempt starts) and are logged, not hidden.
type Dispatcher struct {
	limiter    *bucket // nil: no provider rate limit
	daily      dailyCap
	verified   bool // a verified sender identity is required at claim time
	events     *events.Recorder
	pool       *pgxpool.Pool
	provider   delivery.Provider
	logger     *slog.Logger
	timeout    time.Duration
	bodyMaxAge time.Duration
}

// NewDispatcher builds a Dispatcher. timeout bounds one provider call;
// bodyMaxAge is the safety-net retention for undelivered content.
func NewDispatcher(pool *pgxpool.Pool, p delivery.Provider, logger *slog.Logger, timeout, bodyMaxAge time.Duration) *Dispatcher {
	return &Dispatcher{
		pool: pool, provider: p, logger: logger, timeout: timeout, bodyMaxAge: bodyMaxAge,
		events: events.NewRecorder(pool, logger),
	}
}

// Events returns the recorder every status change of this dispatcher goes
// through (set its status-change hook before the worker starts).
func (d *Dispatcher) Events() *events.Recorder { return d.events }

// LimitProviderRate caps how many messages per second this process hands to
// the provider (0.01 to 10000). Jobs over the budget are snoozed, not failed.
func (d *Dispatcher) LimitProviderRate(perSecond float64) error {
	if !(perSecond >= 0.01 && perSecond <= 10_000) { // also rejects NaN
		return errors.New("provider rate must be between 0.01 and 10000 per second")
	}
	d.limiter = newBucket(perSecond, time.Now)
	return nil
}

// RequireVerifiedSenders makes every send attempt require a verified
// identity of the email's project for its sender (checked again in the
// claim): an identity removed while the email was queued fails it.
func (d *Dispatcher) RequireVerifiedSenders() { d.verified = true }

// dailyCap limits hand-offs to one provider per rolling 24 hours.
type dailyCap struct {
	provider string
	limit    int64 // 0: off
}

// LimitDailyHandoffs caps how many messages all workers together hand to
// provider (the name stored on sent emails) per rolling 24 hours. Jobs over
// the cap are snoozed until a slot frees, not failed. Workers sending at the
// same moment can overshoot by up to their combined concurrency.
func (d *Dispatcher) LimitDailyHandoffs(provider string, limit int) error {
	if provider == "" || limit < 1 {
		return errors.New("daily hand-off limit needs a provider and a limit of at least 1")
	}
	d.daily = dailyCap{provider: provider, limit: int64(limit)}
	return nil
}

// dailyWait returns how long to wait for a free slot under the daily cap
// (0: send now).
func (d *Dispatcher) dailyWait(ctx context.Context) (time.Duration, error) {
	if d.daily.limit == 0 {
		return 0, nil
	}
	q := store.New(d.pool)
	n, err := q.CountProviderHandoffs(ctx, &d.daily.provider)
	if err != nil || n < d.daily.limit {
		return 0, err
	}
	secs, err := q.HandoffLeavesWindowIn(ctx, store.HandoffLeavesWindowInParams{
		Provider: &d.daily.provider, Offset: int32(min(n-d.daily.limit, math.MaxInt32)), //nolint:gosec // bounded by min
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Second, nil // the window moved meanwhile; look again soon
	}
	if err != nil {
		return 0, err
	}
	return max(time.Duration(secs*float64(time.Second)), time.Second), nil
}

// maxDailyJitter spreads jobs waiting for the same daily slot.
const maxDailyJitter = time.Minute

// maxRateWait is the longest a job waits in place for a provider token;
// beyond that it is snoozed and frees its worker slot.
const maxRateWait = time.Second

// SendEmail is the send_email job handler. A nil return ends the job; an
// error makes the queue retry it (the error text is stored by the queue, so
// it is always sanitized).
func (d *Dispatcher) SendEmail(ctx context.Context, args jobs.SendEmailArgs, at jobs.Attempt) error {
	log := d.logger.With(
		slog.String("project_id", args.ProjectID.String()),
		slog.String("email_id", args.EmailID.String()),
		slog.Int64("job_id", at.JobID),
		slog.Int("attempt", at.Number),
	)
	key := store.GetEmailForDispatchParams{ID: args.EmailID, ProjectID: args.ProjectID}
	e, err := store.New(d.pool).GetEmailForDispatch(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		log.WarnContext(ctx, "email for dispatch job not found")
		return nil
	}
	if err != nil {
		return d.internalError(ctx, log, key, at, fmt.Errorf("load email: %w", err))
	}

	switch e.Status {
	case "queued", "sending":
	default:
		return nil // already terminal: a retry after the success was recorded
	}
	if e.ProjectStatus != "active" {
		return d.fail(ctx, log, key, CodeProjectDisabled, CodeProjectDisabled)
	}
	if !e.HasContent {
		return d.fail(ctx, log, key, CodeContentMissing, CodeContentMissing)
	}
	if e.Suppressed {
		// Suppressed after it was queued. A suppression added during the
		// provider call below is not seen (accepted race).
		return d.fail(ctx, log, key, CodeSuppressed, CodeSuppressed)
	}
	if d.verified && !e.SenderVerified {
		return d.fail(ctx, log, key, CodeSenderNotVerified, CodeSenderNotVerified)
	}

	wait, err := d.dailyWait(ctx)
	if err != nil {
		return d.internalError(ctx, log, key, at, fmt.Errorf("daily hand-off count: %w", err))
	}
	if wait > 0 {
		log.InfoContext(ctx, "daily provider limit reached; snoozing", slog.Duration("wait", wait))
		return jobs.Snooze(wait + rand.N(min(wait/10, maxDailyJitter)+time.Second)) //nolint:gosec // scheduling jitter, not security
	}

	if d.limiter != nil {
		wait, ok := d.limiter.TryTake(maxRateWait)
		if !ok {
			return jobs.Snooze(wait + rand.N(wait/10+50*time.Millisecond)) //nolint:gosec // scheduling jitter, not security
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return d.internalError(ctx, log, key, at, fmt.Errorf("provider rate wait: %w", ctx.Err()))
			}
		}
	}

	n, err := store.New(d.pool).MarkSending(ctx, store.MarkSendingParams{ID: key.ID, ProjectID: key.ProjectID, RequireVerifiedSender: d.verified})
	if err != nil {
		return d.internalError(ctx, log, key, at, fmt.Errorf("mark sending: %w", err))
	}
	if n == 0 {
		// Finalized concurrently (e.g. expired), or the project was disabled,
		// the recipient suppressed or the sender identity removed since the
		// email was loaded.
		now, err := store.New(d.pool).GetEmailForDispatch(ctx, key)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return d.internalError(ctx, log, key, at, fmt.Errorf("reload email: %w", err))
		case now.Status != "queued" && now.Status != "sending":
			return nil
		case now.ProjectStatus != "active":
			return d.fail(ctx, log, key, CodeProjectDisabled, CodeProjectDisabled)
		case now.Suppressed:
			return d.fail(ctx, log, key, CodeSuppressed, CodeSuppressed)
		case d.verified && !now.SenderVerified:
			return d.fail(ctx, log, key, CodeSenderNotVerified, CodeSenderNotVerified)
		}
		return nil
	}
	if e.Status == "sending" {
		// A previous attempt was interrupted after possibly reaching the
		// provider (worker crash, shutdown, timeout). This send may duplicate.
		log.WarnContext(ctx, "possible_duplicate_send")
	}

	sendCtx, cancel := context.WithTimeout(ctx, d.timeout)
	res, sendErr := d.provider.Send(sendCtx, delivery.Message{
		ID: e.ID, ProjectID: e.ProjectID, From: e.Sender, To: e.Recipient, Subject: e.Subject,
		Text: deref(e.TextBody), HTML: deref(e.HtmlBody),
	})
	cancel()

	if sendErr == nil {
		return d.finish(ctx, log, key, res, e.Recipient)
	}

	detail := sanitize(sendErr)
	code, retryable := failureCode(sendErr)
	if code == CodeUnsafeTarget {
		log.ErrorContext(ctx, "SMTP target is not Mailpit; refusing to send (check SMTP_ADDR)")
	}
	if !retryable || at.Final() {
		return d.fail(ctx, log, key, code, detail)
	}
	wctx, cancelW := detached(ctx)
	defer cancelW()
	q := store.New(d.pool)
	if notAccepted(sendErr) {
		err = q.RequeueAfterError(wctx, store.RequeueAfterErrorParams{ID: key.ID, ProjectID: key.ProjectID, LastError: &detail, ErrorCode: &code})
	} else {
		err = q.RecordAttemptError(wctx, store.RecordAttemptErrorParams{ID: key.ID, ProjectID: key.ProjectID, LastError: &detail, ErrorCode: &code})
	}
	if err != nil {
		log.ErrorContext(ctx, "record attempt error", slog.Any("error", err))
	}
	log.WarnContext(ctx, "send attempt failed; will retry", slog.String("reason", detail), slog.String("error_code", code))
	return errors.New(detail)
}

// internalError handles a failure outside the provider call (typically the
// database). It is retried; on the final attempt a still-queued email fails,
// so it does not stay open until the expiry safety net. An email left
// "sending" may already be out (an earlier attempt could not record its
// success), so it is not marked failed; the expiry safety net closes it.
func (d *Dispatcher) internalError(ctx context.Context, log *slog.Logger, key store.GetEmailForDispatchParams, at jobs.Attempt, err error) error {
	// A cancelled job (hard shutdown) is interrupted, not failed: the queue
	// puts the attempt back. A job that ran out of time is a failure.
	if !at.Final() || errors.Is(ctx.Err(), context.Canceled) {
		return err
	}
	log.ErrorContext(ctx, "dispatch failed on the final attempt", slog.Any("error", err))
	dctx, cancel := detached(ctx)
	defer cancel()
	code := CodeInternalError
	var n int64
	ferr := pgx.BeginFunc(dctx, d.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		if n, err = q.MarkQueuedFailed(dctx, store.MarkQueuedFailedParams{ID: key.ID, ProjectID: key.ProjectID, LastError: &code, ErrorCode: &code}); err != nil || n == 0 {
			return err
		}
		if err := d.events.Transition(dctx, tx, key.ProjectID, key.ID, events.TypeFailed, "", code); err != nil {
			return err
		}
		return q.DeleteEmailContent(dctx, store.DeleteEmailContentParams{EmailID: key.ID, ProjectID: key.ProjectID})
	})
	if ferr != nil {
		return errors.Join(err, fmt.Errorf("mark failed: %w", ferr))
	}
	if n > 0 {
		log.WarnContext(ctx, "email failed", slog.String("error_code", code), slog.String("reason", code))
	}
	return nil
}

// finish records the provider acceptance and deletes the content in one
// transaction. It runs detached from ctx: the message is already out, so the
// record must not be lost to a shutdown cancellation.
func (d *Dispatcher) finish(ctx context.Context, log *slog.Logger, key store.GetEmailForDispatchParams, res delivery.Result, recipient string) error {
	dctx, cancel := detached(ctx)
	defer cancel()
	var n int64
	err := pgx.BeginFunc(dctx, d.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		n, err = q.MarkDispatched(dctx, store.MarkDispatchedParams{
			ID: key.ID, ProjectID: key.ProjectID, Status: res.Status,
			Provider: &res.Provider, ProviderID: &res.ProviderID,
		})
		if err != nil {
			return err
		}
		if n > 0 {
			if err := d.events.Dispatched(dctx, tx, key.ProjectID, key.ID, res.Status, res.Provider); err != nil {
				return err
			}
		}
		// Deleted in any case: the message is out, whoever finalized it.
		return q.DeleteEmailContent(dctx, store.DeleteEmailContentParams{EmailID: key.ID, ProjectID: key.ProjectID})
	})
	if err != nil {
		// Provider accepted, but we could not record it: the retry will
		// start from "sending" and log possible_duplicate_send.
		return fmt.Errorf("record dispatch: %w", err)
	}
	if n == 0 {
		log.WarnContext(ctx, "email finalized concurrently while sending")
		return nil
	}
	log.InfoContext(ctx, "email dispatched",
		slog.String("status", res.Status),
		slog.String("provider", res.Provider),
		slog.String("recipient", logging.MaskEmail(recipient)),
	)
	return nil
}

// fail marks the email failed with a failure code and a sanitized detail,
// and deletes its content; the job then ends.
func (d *Dispatcher) fail(ctx context.Context, log *slog.Logger, key store.GetEmailForDispatchParams, code, detail string) error {
	dctx, cancel := detached(ctx)
	defer cancel()
	var n int64
	err := pgx.BeginFunc(dctx, d.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		if n, err = q.MarkFailed(dctx, store.MarkFailedParams{ID: key.ID, ProjectID: key.ProjectID, LastError: &detail, ErrorCode: &code}); err != nil {
			return err
		}
		if n > 0 {
			if err := d.events.Transition(dctx, tx, key.ProjectID, key.ID, events.TypeFailed, "", code); err != nil {
				return err
			}
		}
		return q.DeleteEmailContent(dctx, store.DeleteEmailContentParams{EmailID: key.ID, ProjectID: key.ProjectID})
	})
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	if n > 0 {
		log.WarnContext(ctx, "email failed", slog.String("error_code", code), slog.String("reason", detail))
	}
	return nil
}

// Cleanup is the periodic safety net: emails stuck before a terminal state
// longer than bodyMaxAge are failed, and any content older than that is
// deleted (covers crashes or panics on a final attempt). Expired idempotency
// keys are removed too.
func (d *Dispatcher) Cleanup(ctx context.Context) error {
	cutoff := time.Now().Add(-d.bodyMaxAge)
	var expired, deleted int64
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		rows, err := q.ExpireOpenEmails(ctx, cutoff)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := d.events.Transition(ctx, tx, r.ProjectID, r.ID, events.TypeFailed, "", CodeExpired); err != nil {
				return err
			}
		}
		expired = int64(len(rows))
		deleted, err = q.DeleteContentsBefore(ctx, cutoff)
		return err
	})
	if err != nil {
		return fmt.Errorf("cleanup emails: %w", err)
	}
	// Separate statement: a problem with the key table must not hold back
	// expiry and content deletion.
	keys, err := store.New(d.pool).DeleteIdempotencyKeysOlderThan(ctx, IdempotencyKeyTTL.Seconds())
	if err != nil {
		return fmt.Errorf("cleanup idempotency keys: %w", err)
	}
	windows, err := store.New(d.pool).DeleteRateLimitWindowsBefore(ctx)
	if err != nil {
		return fmt.Errorf("cleanup rate limit windows: %w", err)
	}
	if expired > 0 || deleted > 0 || keys > 0 || windows > 0 {
		d.logger.InfoContext(ctx, "email cleanup", slog.Int64("expired", expired),
			slog.Int64("contents_deleted", deleted), slog.Int64("idempotency_keys_deleted", keys), slog.Int64("rate_limit_windows_deleted", windows))
	}
	return nil
}

// terminalWriteTimeout bounds state writes that run detached from the job
// context (they must survive shutdown, but never hang forever).
const terminalWriteTimeout = 10 * time.Second

func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
}

// sanitize turns an error into text safe to store and log.
func sanitize(err error) string {
	var de *delivery.Error
	if errors.As(err, &de) {
		return de.Error()
	}
	return "delivery failed: internal"
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
