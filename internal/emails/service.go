package emails

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
)

// Enqueuer inserts the dispatch job inside the caller's transaction.
type Enqueuer interface {
	EnqueueSendEmail(ctx context.Context, tx pgx.Tx, args jobs.SendEmailArgs) error
}

// Email is the client-visible state of a send request.
type Email struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	Status     string
	From       string
	To         string
	Subject    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
	// Attempts counts dispatch attempts started so far.
	Attempts int
	// ErrorCode is the failure code of the last failed attempt (nil if none
	// or after a successful send).
	ErrorCode *string
	// Replayed is set by Create when the request repeated an earlier one
	// with the same idempotency key: the email is that earlier one.
	Replayed bool
}

func fromRow(r store.Email) Email {
	return Email{
		ID: r.ID, ProjectID: r.ProjectID, Status: r.Status, From: r.Sender, To: r.Recipient,
		Subject: r.Subject, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt,
		Attempts: int(r.Attempts), ErrorCode: r.ErrorCode,
	}
}

// Service accepts and reads emails.
type Service struct {
	pool      *pgxpool.Pool
	enq       Enqueuer
	logger    *slog.Logger
	perMinute int
}

// NewService builds a Service that accepts at most perMinute sends per
// project and minute (values below 1 are treated as 1).
func NewService(pool *pgxpool.Pool, enq Enqueuer, logger *slog.Logger, perMinute int) *Service {
	return &Service{pool: pool, enq: enq, logger: logger, perMinute: max(perMinute, 1)}
}

// RateLimitError means the project used up its sends for the current minute.
type RateLimitError struct {
	RetryAfter time.Duration // until the current window ends, 1s–60s
}

func (e *RateLimitError) Error() string { return "send rate limit exceeded" }

// Create validates the request and commits the email, its content and its
// dispatch job atomically. Only a nil error means the email is persisted.
//
// With an idempotency key, a repeated request with the same payload returns
// the email of the first request (Replayed set) and creates nothing; a
// different payload returns ErrIdempotencyKeyReused. The key is claimed in the
// same transaction as the email, so concurrent duplicates create one email.
func (s *Service) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (Email, error) {
	if err := in.Validate(); err != nil {
		return Email{}, err
	}
	var key *store.GetIdempotencyKeyParams
	var fingerprint []byte
	if in.IdempotencyKey != "" {
		key = &store.GetIdempotencyKeyParams{ProjectID: projectID, Operation: operationCreateEmail, Key: in.IdempotencyKey}
		fingerprint = in.Fingerprint()
	}

	var row store.Email
	var replayOf uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if key != nil {
			if err := q.DeleteExpiredIdempotencyKey(ctx, store.DeleteExpiredIdempotencyKeyParams{
				ProjectID: key.ProjectID, Operation: key.Operation, Key: key.Key, TtlSeconds: IdempotencyKeyTTL.Seconds(),
			}); err != nil {
				return fmt.Errorf("expire idempotency key: %w", err)
			}
			id, found, err := matchKey(ctx, q, *key, fingerprint)
			if err != nil || found {
				replayOf = id
				return err
			}
		}
		var err error
		row, err = q.InsertEmail(ctx, store.InsertEmailParams{
			ProjectID: projectID, Sender: in.From, Recipient: in.To, Subject: in.Subject,
		})
		if err != nil {
			return fmt.Errorf("insert email: %w", err)
		}
		if err := q.InsertEmailContent(ctx, store.InsertEmailContentParams{
			EmailID: row.ID, ProjectID: projectID, TextBody: nonEmpty(in.Text), HtmlBody: nonEmpty(in.HTML),
		}); err != nil {
			return fmt.Errorf("insert email content: %w", err)
		}
		if err := s.enq.EnqueueSendEmail(ctx, tx, jobs.SendEmailArgs{EmailID: row.ID, ProjectID: projectID}); err != nil {
			return err
		}
		if key != nil {
			// Waits for a concurrent transaction holding the same key; no row
			// means that one committed first.
			n, err := q.InsertIdempotencyKey(ctx, store.InsertIdempotencyKeyParams{
				ProjectID: key.ProjectID, Operation: key.Operation, Key: key.Key, Fingerprint: fingerprint, EmailID: row.ID,
			})
			if err != nil {
				return fmt.Errorf("insert idempotency key: %w", err)
			}
			if n == 0 {
				return errKeyTaken
			}
		}
		// Refusals come after the idempotency key is claimed, so a duplicate
		// of an accepted request replays instead of being refused.
		suppressed, err := q.IsSuppressed(ctx, store.IsSuppressedParams{ProjectID: projectID, Recipient: in.To})
		if err != nil {
			return fmt.Errorf("check suppression: %w", err)
		}
		if suppressed {
			return ErrRecipientSuppressed
		}
		// Counted last: a request is only refused after its idempotency key
		// is claimed, so a duplicate replays instead of being limited, and
		// the window row is locked only briefly.
		c, err := q.CountSend(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count send: %w", err)
		}
		if int(c.Count) > s.perMinute {
			return &RateLimitError{RetryAfter: time.Duration(min(max(c.RetryAfterSeconds, 1), 60)) * time.Second}
		}
		return nil
	})
	if errors.Is(err, errKeyTaken) {
		// Rolled back; the winner's key is committed and visible now.
		var found bool
		replayOf, found, err = matchKey(ctx, store.New(s.pool), *key, fingerprint)
		if err == nil && !found {
			err = errors.New("idempotency key vanished after a conflict")
		}
	}
	if err != nil {
		return Email{}, err
	}
	if replayOf != uuid.Nil {
		e, err := s.Get(ctx, projectID, replayOf)
		if err != nil {
			return Email{}, fmt.Errorf("load replayed email: %w", err)
		}
		e.Replayed = true
		s.logger.InfoContext(ctx, "idempotent replay",
			slog.String("project_id", projectID.String()),
			slog.String("email_id", e.ID.String()),
		)
		return e, nil
	}
	s.logger.InfoContext(ctx, "email queued",
		slog.String("project_id", projectID.String()),
		slog.String("email_id", row.ID.String()),
		slog.String("recipient", logging.MaskEmail(in.To)),
	)
	return fromRow(row), nil
}

// errKeyTaken: a concurrent request committed the same idempotency key first.
var errKeyTaken = errors.New("idempotency key taken concurrently")

// matchKey looks up an idempotency key. found reports whether it exists; a
// stored fingerprint that differs from fingerprint is ErrIdempotencyKeyReused.
func matchKey(ctx context.Context, q *store.Queries, key store.GetIdempotencyKeyParams, fingerprint []byte) (uuid.UUID, bool, error) {
	k, err := q.GetIdempotencyKey(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("get idempotency key: %w", err)
	}
	if !bytes.Equal(k.Fingerprint, fingerprint) {
		return uuid.Nil, true, ErrIdempotencyKeyReused
	}
	return k.EmailID, true, nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ErrNotFound means no email with this id exists for the project. Emails of
// other projects are reported the same way.
var ErrNotFound = errors.New("email not found")

// Get returns an email of the project.
func (s *Service) Get(ctx context.Context, projectID, id uuid.UUID) (Email, error) {
	row, err := store.New(s.pool).GetEmail(ctx, store.GetEmailParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Email{}, ErrNotFound
	}
	if err != nil {
		return Email{}, fmt.Errorf("get email: %w", err)
	}
	return fromRow(row), nil
}
