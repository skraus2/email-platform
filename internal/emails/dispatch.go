package emails

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/delivery"
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
	pool       *pgxpool.Pool
	provider   delivery.Provider
	logger     *slog.Logger
	timeout    time.Duration
	bodyMaxAge time.Duration
}

// NewDispatcher builds a Dispatcher. timeout bounds one provider call;
// bodyMaxAge is the safety-net retention for undelivered content.
func NewDispatcher(pool *pgxpool.Pool, p delivery.Provider, logger *slog.Logger, timeout, bodyMaxAge time.Duration) *Dispatcher {
	return &Dispatcher{pool: pool, provider: p, logger: logger, timeout: timeout, bodyMaxAge: bodyMaxAge}
}

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
		return fmt.Errorf("load email: %w", err)
	}

	switch e.Status {
	case "queued":
	case "sending":
		// A previous attempt was interrupted after possibly reaching the
		// provider (worker crash, shutdown, timeout). This send may duplicate.
		log.WarnContext(ctx, "possible_duplicate_send")
	default:
		return nil // already terminal: a retry after the success was recorded
	}
	if e.ProjectStatus != "active" {
		return d.fail(ctx, log, key, "project_disabled")
	}
	if !e.HasContent {
		return d.fail(ctx, log, key, "content_missing")
	}

	n, err := store.New(d.pool).MarkSending(ctx, store.MarkSendingParams(key))
	if err != nil {
		return fmt.Errorf("mark sending: %w", err)
	}
	if n == 0 {
		return nil // finalized concurrently (e.g. expired)
	}

	sendCtx, cancel := context.WithTimeout(ctx, d.timeout)
	res, sendErr := d.provider.Send(sendCtx, delivery.Message{
		ID: e.ID, From: e.Sender, To: e.Recipient, Subject: e.Subject,
		Text: deref(e.TextBody), HTML: deref(e.HtmlBody),
	})
	cancel()

	if sendErr == nil {
		return d.finish(ctx, log, key, res, e.Recipient)
	}

	reason := sanitize(sendErr)
	var de *delivery.Error
	permanent := errors.As(sendErr, &de) && !de.Temporary()
	if permanent || at.Final() {
		return d.fail(ctx, log, key, reason)
	}
	wctx, cancelW := detached(ctx)
	defer cancelW()
	q := store.New(d.pool)
	if notAccepted(sendErr) {
		err = q.RequeueAfterError(wctx, store.RequeueAfterErrorParams{ID: key.ID, ProjectID: key.ProjectID, LastError: &reason})
	} else {
		err = q.RecordAttemptError(wctx, store.RecordAttemptErrorParams{ID: key.ID, ProjectID: key.ProjectID, LastError: &reason})
	}
	if err != nil {
		log.ErrorContext(ctx, "record attempt error", slog.Any("error", err))
	}
	log.WarnContext(ctx, "send attempt failed; will retry", slog.String("reason", reason))
	return errors.New(reason)
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

// fail marks the email failed and deletes its content; the job then ends.
func (d *Dispatcher) fail(ctx context.Context, log *slog.Logger, key store.GetEmailForDispatchParams, reason string) error {
	dctx, cancel := detached(ctx)
	defer cancel()
	err := pgx.BeginFunc(dctx, d.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.MarkFailed(dctx, store.MarkFailedParams{ID: key.ID, ProjectID: key.ProjectID, LastError: &reason}); err != nil {
			return err
		}
		return q.DeleteEmailContent(dctx, store.DeleteEmailContentParams{EmailID: key.ID, ProjectID: key.ProjectID})
	})
	if err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	log.WarnContext(ctx, "email failed", slog.String("reason", reason))
	return nil
}

// Cleanup is the periodic safety net: emails stuck before a terminal state
// longer than bodyMaxAge are failed, and any content older than that is
// deleted (covers crashes or panics on a final attempt).
func (d *Dispatcher) Cleanup(ctx context.Context) error {
	cutoff := time.Now().Add(-d.bodyMaxAge)
	var expired, deleted int64
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		if expired, err = q.ExpireOpenEmails(ctx, cutoff); err != nil {
			return err
		}
		deleted, err = q.DeleteContentsBefore(ctx, cutoff)
		return err
	})
	if err != nil {
		return fmt.Errorf("cleanup emails: %w", err)
	}
	if expired > 0 || deleted > 0 {
		d.logger.InfoContext(ctx, "email cleanup", slog.Int64("expired", expired), slog.Int64("contents_deleted", deleted))
	}
	return nil
}

// terminalWriteTimeout bounds state writes that run detached from the job
// context (they must survive shutdown, but never hang forever).
const terminalWriteTimeout = 10 * time.Second

func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
}

// notAccepted reports whether the provider certainly did not take the
// message (connection refused, or an SMTP rejection reply).
func notAccepted(err error) bool {
	var de *delivery.Error
	return errors.As(err, &de) && (de.Class == delivery.ClassConnect || de.Class == delivery.ClassRejected)
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
