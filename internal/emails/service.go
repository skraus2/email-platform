package emails

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/events"
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
	// requireSender: from must match a verified sender identity.
	requireSender bool
	// List query limits (see defaultListSlots).
	listSlots   chan struct{}
	listWait    time.Duration
	listTimeout time.Duration
}

// NewService builds a Service that accepts at most perMinute sends per
// project and minute (values below 1 are treated as 1).
func NewService(pool *pgxpool.Pool, enq Enqueuer, logger *slog.Logger, perMinute int) *Service {
	return &Service{
		pool: pool, enq: enq, logger: logger, perMinute: max(perMinute, 1),
		listSlots: make(chan struct{}, defaultListSlots), listWait: defaultListWait, listTimeout: defaultListTimeout,
	}
}

// RequireVerifiedSenders makes Create refuse a from address that matches
// no verified sender identity of the project (ErrSenderNotVerified).
func (s *Service) RequireVerifiedSenders() { s.requireSender = true }

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
		// Cheap read-only pre-checks: a request that will be refused does no
		// write work and does not queue on the project's window row, so one
		// project hammering the API cannot tie up connections for others.
		// The checks after the key claim below stay authoritative.
		if err := s.refuse(ctx, q, projectID, in, false); err != nil {
			var limited *RateLimitError
			if key != nil && (errors.Is(err, ErrSenderNotVerified) || errors.Is(err, ErrRecipientSuppressed) || errors.As(err, &limited)) {
				// A concurrent request with the same key may have committed
				// (and filled the window) after the lookup above: replay it
				// rather than refuse a duplicate of an accepted request.
				// Not covered: a winner that has not committed yet when a
				// suppression is added, or one counting in the next minute;
				// the refusal is then retry-safe with the same key.
				id, found, kerr := matchKey(ctx, q, *key, fingerprint)
				if kerr != nil || found {
					replayOf = id
					return kerr
				}
			}
			return err
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
		// of an accepted request replays instead of being refused. The send
		// is counted last, so the window row is locked only briefly.
		return s.refuse(ctx, q, projectID, in, true)
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

// refuse checks the sender identity (when required), suppression and the
// project's rate limit, in that order. With count set it counts this send
// (locking the window row until the transaction ends); otherwise it only
// reads the committed count.
func (s *Service) refuse(ctx context.Context, q *store.Queries, projectID uuid.UUID, in CreateInput, count bool) error {
	if s.requireSender {
		ok, err := senderVerified(ctx, q, projectID, in.From)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSenderNotVerified
		}
	}
	suppressed, err := q.IsSuppressed(ctx, store.IsSuppressedParams{ProjectID: projectID, Recipient: in.To})
	if err != nil {
		return fmt.Errorf("check suppression: %w", err)
	}
	if suppressed {
		return ErrRecipientSuppressed
	}
	var n, retryAfter int32
	if count {
		c, err := q.CountSend(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count send: %w", err)
		}
		// This send is included in c.Count.
		n, retryAfter = c.Count-1, c.RetryAfterSeconds
	} else {
		c, err := q.GetSendCount(ctx, projectID)
		if err != nil {
			return fmt.Errorf("read send count: %w", err)
		}
		n, retryAfter = c.Count, c.RetryAfterSeconds
	}
	if int(n) >= s.perMinute {
		return &RateLimitError{RetryAfter: time.Duration(min(max(retryAfter, 1), 60)) * time.Second}
	}
	return nil
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

// MaxEmailPage bounds one page of a project's email list.
const MaxEmailPage = 100

// ListFilter narrows a project's email list. Zero values mean "any".
type ListFilter struct {
	Statuses      []string
	Recipient     string     // exact address, case-insensitive
	CreatedAfter  *time.Time // inclusive
	CreatedBefore *time.Time // exclusive
	// After is the cursor: only emails with a smaller id follow (uuid.Nil:
	// from the newest).
	After uuid.UUID
}

// IsStatus reports whether s is an email status.
func IsStatus(s string) bool { return slices.Contains(statuses, s) }

// ErrListBusy means a list query could not run now: all list slots stayed
// taken for the wait time, or the query ran past its time limit.
var ErrListBusy = errors.New("email list busy")

// List query limits: at most listSlots run at once per process (so that list
// requests cannot take the whole connection pool), a request waits at most
// listWait for a slot, and a query is cancelled after listTimeout.
const (
	defaultListSlots   = 2
	defaultListWait    = time.Second
	defaultListTimeout = 5 * time.Second
)

// List returns up to limit (1–MaxEmailPage) emails of the project, newest
// first, matching f. more reports whether further emails follow.
func (s *Service) List(ctx context.Context, projectID uuid.UUID, f ListFilter, limit int) (page []Email, more bool, err error) {
	if limit < 1 || limit > MaxEmailPage {
		return nil, false, fmt.Errorf("email page size must be between 1 and %d", MaxEmailPage)
	}
	wait := time.NewTimer(s.listWait)
	defer wait.Stop()
	select {
	case s.listSlots <- struct{}{}:
		defer func() { <-s.listSlots }()
	case <-wait.C:
		return nil, false, ErrListBusy
	case <-ctx.Done():
		return nil, false, fmt.Errorf("list emails: %w", ctx.Err())
	}
	qctx, cancel := context.WithTimeout(ctx, s.listTimeout)
	defer cancel()

	before := f.After
	if before == uuid.Nil {
		before = uuid.Max
	}
	minID := uuid.Nil
	if f.CreatedAfter != nil {
		minID = uuidV7Floor(*f.CreatedAfter)
	}
	statuses := f.Statuses
	if statuses == nil {
		statuses = []string{}
	}
	rowLimit := int32(limit + 1) //nolint:gosec // bounded above
	var rows []store.Email
	q := store.New(s.pool)
	if f.Recipient != "" {
		rows, err = q.ListProjectEmailsByRecipient(qctx, store.ListProjectEmailsByRecipientParams{
			ProjectID: projectID, Recipient: f.Recipient, BeforeID: before, MinID: minID, Statuses: statuses,
			CreatedAfter: f.CreatedAfter, CreatedBefore: f.CreatedBefore, RowLimit: rowLimit,
		})
	} else {
		rows, err = q.ListProjectEmails(qctx, store.ListProjectEmailsParams{
			ProjectID: projectID, BeforeID: before, MinID: minID, Statuses: statuses,
			CreatedAfter: f.CreatedAfter, CreatedBefore: f.CreatedBefore, RowLimit: rowLimit,
		})
	}
	if err != nil {
		if ctx.Err() == nil && qctx.Err() != nil {
			s.logger.WarnContext(ctx, "email list query hit its time limit",
				slog.String("project_id", projectID.String()), slog.Duration("limit", s.listTimeout))
			return nil, false, ErrListBusy
		}
		return nil, false, fmt.Errorf("list emails: %w", err)
	}
	page = make([]Email, 0, min(len(rows), limit))
	for _, r := range rows[:min(len(rows), limit)] {
		page = append(page, fromRow(r))
	}
	return page, len(rows) > limit, nil
}

// uuidV7Floor is the smallest UUIDv7 with a timestamp at or after the
// millisecond of t. Email ids are generated by the database while inserting,
// after the transaction start that sets created_at, so an email created at
// or after t never has a smaller id.
func uuidV7Floor(t time.Time) uuid.UUID {
	ms := t.UnixMilli()
	if ms < 0 {
		return uuid.Nil
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(ms)) //nolint:gosec // ms >= 0 checked above
	var u uuid.UUID
	copy(u[:6], b[2:]) // 48-bit millisecond timestamp
	u[6] = 0x70
	u[8] = 0x80
	return u
}

// MaxEventPage bounds one page of an email's event history.
const MaxEventPage = 100

// Events returns up to limit (1–MaxEventPage) events of an email of the
// project after the event after (uuid.Nil: from the start). more reports
// whether further events follow. Emails of other projects are ErrNotFound.
func (s *Service) Events(ctx context.Context, projectID, id, after uuid.UUID, limit int) (page []events.Event, more bool, err error) {
	if limit < 1 || limit > MaxEventPage {
		return nil, false, fmt.Errorf("event page size must be between 1 and %d", MaxEventPage)
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.GetEmail(ctx, store.GetEmailParams{ID: id, ProjectID: projectID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("get email: %w", err)
		}
		var err error
		page, err = events.List(ctx, q, projectID, id, after, limit+1)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if len(page) > limit {
		return page[:limit], true, nil
	}
	return page, false, nil
}
