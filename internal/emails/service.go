package emails

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
)

// Enqueuer inserts the dispatch job inside the caller's transaction.
type Enqueuer interface {
	EnqueueSendEmail(ctx context.Context, tx pgx.Tx, args jobs.SendEmailArgs, opts jobs.SendOpts) error
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
	// TemplateID and TemplateVersion name the template version a template
	// send was rendered from (nil for raw sends).
	TemplateID      *uuid.UUID
	TemplateVersion *int
	// ScheduledAt is the planned send time (nil: immediate).
	ScheduledAt *time.Time
	// BroadcastID names the broadcast of a recipient email (nil otherwise).
	BroadcastID *uuid.UUID
	// Replayed is set by Create when the request repeated an earlier one
	// with the same idempotency key: the email is that earlier one.
	Replayed bool
}

func fromRow(r store.Email) Email {
	e := Email{
		ID: r.ID, ProjectID: r.ProjectID, Status: r.Status, From: r.Sender, To: r.Recipient,
		Subject: r.Subject, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt,
		Attempts: int(r.Attempts), ErrorCode: r.ErrorCode, TemplateID: r.TemplateID, ScheduledAt: r.ScheduledAt, BroadcastID: r.BroadcastID,
	}
	if r.TemplateVersion != nil {
		v := int(*r.TemplateVersion)
		e.TemplateVersion = &v
	}
	return e
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
	// Template sends: compiled versions are cached; at most renderSlots
	// compile/render at once per process, waiting at most renderWait.
	compiled    *compiledCache
	compiles    atomic.Int64
	renderSlots chan struct{}
	renderWait  time.Duration
	// scheduleAhead bounds how far in the future a send may be planned;
	// scheduledOpen caps a project's planned sends still waiting.
	scheduleAhead time.Duration
	scheduledOpen int
	// events records cancellations (with the webhook hook of the process).
	events *events.Recorder
}

// DefaultScheduleMaxAhead is how far ahead a send may be planned unless
// LimitScheduleAhead sets less; content of a planned send is kept until
// then.
const DefaultScheduleMaxAhead = 720 * time.Hour

// DefaultScheduleMaxOpen caps a project's waiting planned sends unless
// LimitScheduledOpen sets another value.
const DefaultScheduleMaxOpen = 10_000

// ErrScheduleLimit means the project already has as many planned sends
// waiting as it may.
var ErrScheduleLimit = errors.New("scheduled send limit reached")

// Template render limits per process. A compile of the largest version
// briefly holds about 60 MiB of heap, so few run at once; the cache keeps at
// most 1 MiB of source (about 60 MiB of heap) and skips versions over
// 64 KiB.
const (
	compiledCacheBytes      = 1 << 20
	compiledCacheEntryBytes = 64 << 10
	defaultRenderSlots      = 2
	defaultRenderWait       = time.Second
)

// NewService builds a Service that accepts at most perMinute sends per
// project and minute (values below 1 are treated as 1).
func NewService(pool *pgxpool.Pool, enq Enqueuer, logger *slog.Logger, perMinute int) *Service {
	return &Service{
		pool: pool, enq: enq, logger: logger, perMinute: max(perMinute, 1),
		listSlots: make(chan struct{}, defaultListSlots), listWait: defaultListWait, listTimeout: defaultListTimeout,
		compiled:    newCompiledCache(compiledCacheBytes, compiledCacheEntryBytes),
		renderSlots: make(chan struct{}, defaultRenderSlots), renderWait: defaultRenderWait,
		scheduleAhead: DefaultScheduleMaxAhead, scheduledOpen: DefaultScheduleMaxOpen, events: events.NewRecorder(pool, logger),
	}
}

// LimitScheduleAhead sets how far in the future a send may be planned
// (at most DefaultScheduleMaxAhead).
func (s *Service) LimitScheduleAhead(d time.Duration) {
	s.scheduleAhead = min(max(d, time.Minute), DefaultScheduleMaxAhead)
}

// LimitScheduledOpen caps how many planned sends of a project may wait at
// once (values below 1 are treated as 1). The count is not locked:
// concurrent requests can overshoot by their number.
func (s *Service) LimitScheduledOpen(n int) { s.scheduledOpen = max(n, 1) }

// Recorder returns the recorder cancellations go through (set its
// status-change hook before the service is used).
func (s *Service) Recorder() *events.Recorder { return s.events }

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
	// The horizon and the cap on waiting planned sends are checked in refuse,
	// after the idempotency lookup, so a retry of an accepted request still
	// replays after the limits were lowered.
	var scheduledAt *time.Time
	if in.ScheduledAt != nil && in.ScheduledAt.After(time.Now()) {
		at := in.ScheduledAt.UTC().Truncate(time.Microsecond)
		scheduledAt, in.planned = &at, true
	}
	var key *store.GetIdempotencyKeyParams
	var fingerprint []byte
	if in.IdempotencyKey != "" {
		key = &store.GetIdempotencyKeyParams{ProjectID: projectID, Operation: operationCreateEmail, Key: in.IdempotencyKey}
		if in.TemplateID == uuid.Nil {
			fingerprint = in.Fingerprint()
		} else {
			var err error
			if fingerprint, err = in.TemplateFingerprint(); err != nil {
				return Email{}, &templates.Error{Code: templates.CodeVariablesInvalid, Field: "variables", Reason: err.Error()}
			}
		}
	}
	var templateID *uuid.UUID
	var templateVersion *int32
	if in.TemplateID != uuid.Nil {
		// Rendering costs real work, so it runs only for a request that is
		// neither a replay nor refused: a retry stays a replay even if the
		// published version changed meanwhile. The checks in the transaction
		// below stay authoritative.
		q := store.New(s.pool)
		if key != nil {
			if err := q.DeleteExpiredIdempotencyKey(ctx, store.DeleteExpiredIdempotencyKeyParams{
				ProjectID: key.ProjectID, Operation: key.Operation, Key: key.Key, TtlSeconds: IdempotencyKeyTTL.Seconds(),
			}); err != nil {
				return Email{}, fmt.Errorf("expire idempotency key: %w", err)
			}
			id, found, err := matchKey(ctx, q, *key, fingerprint)
			if err != nil {
				return Email{}, err
			}
			if found {
				return s.replay(ctx, projectID, id)
			}
		}
		if err := s.refuse(ctx, q, projectID, in, false); err != nil {
			id, err := winnerAfterRefusal(ctx, q, key, fingerprint, err)
			if err != nil {
				return Email{}, err
			}
			return s.replay(ctx, projectID, id)
		}
		r, version, err := s.render(ctx, projectID, in)
		if err != nil {
			return Email{}, err
		}
		in.Subject, in.Text, in.HTML = r.Subject, r.Text, r.HTML
		id, v := in.TemplateID, int32(version) //nolint:gosec // versions are capped far below 2^31
		templateID, templateVersion = &id, &v
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
			replayOf, err = winnerAfterRefusal(ctx, q, key, fingerprint, err)
			return err
		}
		var err error
		row, err = q.InsertEmail(ctx, store.InsertEmailParams{
			ProjectID: projectID, Sender: in.From, Recipient: in.To, Subject: in.Subject,
			TemplateID: templateID, TemplateVersion: templateVersion, ScheduledAt: scheduledAt,
		})
		if err != nil {
			return fmt.Errorf("insert email: %w", err)
		}
		if err := q.InsertEmailContent(ctx, store.InsertEmailContentParams{
			EmailID: row.ID, ProjectID: projectID, TextBody: nonEmpty(in.Text), HtmlBody: nonEmpty(in.HTML),
		}); err != nil {
			return fmt.Errorf("insert email content: %w", err)
		}
		var opts jobs.SendOpts
		if scheduledAt != nil {
			opts.ScheduledAt = *scheduledAt
		}
		if err := s.enq.EnqueueSendEmail(ctx, tx, jobs.SendEmailArgs{EmailID: row.ID, ProjectID: projectID}, opts); err != nil {
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
		return s.replay(ctx, projectID, replayOf)
	}
	s.logger.InfoContext(ctx, "email queued",
		slog.String("project_id", projectID.String()),
		slog.String("email_id", row.ID.String()),
		slog.String("recipient", logging.MaskEmail(in.To)),
	)
	return fromRow(row), nil
}

func (s *Service) replay(ctx context.Context, projectID, id uuid.UUID) (Email, error) {
	e, err := s.Get(ctx, projectID, id)
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

// ErrTemplateNotFound: the template or version does not exist in the
// project (also for templates of other projects, and for "published" on a
// template without versions).
var ErrTemplateNotFound = errors.New("template not found")

// ErrRenderBusy: all template render slots stayed taken for the wait time.
var ErrRenderBusy = errors.New("template rendering busy")

// render resolves the requested version of a template of the project and
// renders it with the request variables. Variables are checked against the
// version's schema before any template work; compiled versions are cached.
// Variable and output errors are *templates.Error; variables are never
// logged.
func (s *Service) render(ctx context.Context, projectID uuid.UUID, in CreateInput) (templates.Rendered, int, error) {
	v, err := templates.Resolve(ctx, store.New(s.pool), projectID, in.TemplateID, in.TemplateVersion)
	if errors.Is(err, templates.ErrNotFound) {
		return templates.Rendered{}, 0, ErrTemplateNotFound
	}
	if err != nil {
		return templates.Rendered{}, 0, fmt.Errorf("resolve template: %w", err)
	}
	// A stored version is server state: its problems are internal errors,
	// never client errors (not wrapped, so no *templates.Error escapes).
	schema, err := templates.ParseSchema(v.Schema)
	if err != nil {
		return templates.Rendered{}, 0, fmt.Errorf("stored schema of template %s version %d: %v", v.TemplateID, v.Version, err) //nolint:errorlint // deliberately not wrapped
	}
	if err := templates.ValidateVariables(schema, in.Variables); err != nil {
		return templates.Rendered{}, 0, err
	}

	wait := time.NewTimer(s.renderWait)
	defer wait.Stop()
	slots := s.renderSlots
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-wait.C:
		return templates.Rendered{}, 0, ErrRenderBusy
	case <-ctx.Done():
		return templates.Rendered{}, 0, fmt.Errorf("render template: %w", ctx.Err())
	}
	key := compiledKey{project: projectID, template: v.TemplateID, version: v.Version}
	c := s.compiled.get(key)
	if c == nil {
		s.compiles.Add(1)
		if c, err = v.Compile(); err != nil {
			return templates.Rendered{}, 0, fmt.Errorf("%v", err) //nolint:errorlint // deliberately not wrapped: a stored version is server state
		}
		s.compiled.put(key, c, len(v.Source.Subject)+len(v.Source.Text)+len(v.Source.HTML))
	}
	r, err := c.Render(in.Variables)
	if err != nil {
		return templates.Rendered{}, 0, err
	}
	return r, v.Version, nil
}

// winnerAfterRefusal: with a key, a refusal (sender, suppression, rate limit)
// may hide a concurrent request with the same key that committed — and
// filled the window — after the key lookup. Its email is returned for a
// replay rather than refusing a duplicate of an accepted request. Not
// covered: a winner that has not committed yet when a suppression is added,
// or one counting in the next minute; the refusal is then retry-safe with
// the same key. Otherwise the refusal is returned unchanged.
func winnerAfterRefusal(ctx context.Context, q *store.Queries, key *store.GetIdempotencyKeyParams, fingerprint []byte, refusal error) (uuid.UUID, error) {
	var limited *RateLimitError
	retrySafe := errors.Is(refusal, ErrSenderNotVerified) || errors.Is(refusal, ErrRecipientSuppressed) || errors.As(refusal, &limited) ||
		errors.Is(refusal, ErrScheduleLimit)
	if key == nil || !retrySafe {
		return uuid.Nil, refusal
	}
	id, found, err := matchKey(ctx, q, *key, fingerprint)
	switch {
	case err != nil:
		return uuid.Nil, err
	case found:
		return id, nil
	}
	return uuid.Nil, refusal
}

// refuse checks the sender identity (when required), the schedule horizon
// and the cap on waiting planned sends, suppression and the project's rate
// limit, in that order. With count set it counts this send
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
	if in.ScheduledAt != nil && in.ScheduledAt.After(time.Now().Add(s.scheduleAhead)) {
		return invalid("scheduled_at", fmt.Sprintf("must be at most %s ahead", s.scheduleAhead))
	}
	if in.planned {
		n, err := q.CountOpenScheduled(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count scheduled sends: %w", err)
		}
		if count {
			n-- // this email, inserted just before
		}
		if n >= int64(s.scheduledOpen) {
			return ErrScheduleLimit
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

// ErrNotCancelable means the email is no longer queued: a worker claimed it
// or it reached a final state.
var ErrNotCancelable = errors.New("email can no longer be cancelled")

// Cancel cancels a queued email of the project: it becomes canceled, its
// content is deleted and a canceled event is recorded, all in one
// transaction. The claim of a worker and Cancel exclude each other through
// the status.
func (s *Service) Cancel(ctx context.Context, projectID, id uuid.UUID) (Email, error) {
	var row store.Email
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		row, err = q.CancelEmail(ctx, store.CancelEmailParams{ID: id, ProjectID: projectID})
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := q.GetEmail(ctx, store.GetEmailParams{ID: id, ProjectID: projectID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return fmt.Errorf("get email: %w", err)
			}
			return ErrNotCancelable
		}
		if err != nil {
			return fmt.Errorf("cancel email: %w", err)
		}
		if err := q.DeleteEmailContent(ctx, store.DeleteEmailContentParams{EmailID: id, ProjectID: projectID}); err != nil {
			return fmt.Errorf("delete content: %w", err)
		}
		return s.events.Transition(ctx, tx, projectID, id, events.TypeCanceled, "", "")
	})
	if err != nil {
		return Email{}, err
	}
	s.logger.InfoContext(ctx, "email canceled",
		slog.String("project_id", projectID.String()), slog.String("email_id", id.String()))
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
	TemplateID    *uuid.UUID // only emails sent from this template
	BroadcastID   *uuid.UUID // only recipient emails of this broadcast
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
	// The client deadline has a margin over the server's statement timeout,
	// so a slow query normally ends with the server's cancel and the
	// connection stays reusable.
	qctx, cancel := context.WithTimeout(ctx, s.listTimeout+time.Second)
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
	// Unnamed statements (the plan must fit this request's filters and
	// cursor, not a cached generic plan) under a server-side statement
	// timeout, so an expensive list is really stopped, not just abandoned.
	err = store.ReadOnlyWithTimeout(qctx, s.pool, s.listTimeout, func(q *store.Queries) error {
		var err error
		if f.Recipient != "" {
			rows, err = q.ListProjectEmailsByRecipient(qctx, store.ListProjectEmailsByRecipientParams{
				ProjectID: projectID, Recipient: f.Recipient, BeforeID: before, MinID: minID, Statuses: statuses,
				CreatedAfter: f.CreatedAfter, CreatedBefore: f.CreatedBefore, TemplateID: f.TemplateID, BroadcastID: f.BroadcastID, RowLimit: rowLimit,
			})
		} else {
			rows, err = q.ListProjectEmails(qctx, store.ListProjectEmailsParams{
				ProjectID: projectID, BeforeID: before, MinID: minID, Statuses: statuses,
				CreatedAfter: f.CreatedAfter, CreatedBefore: f.CreatedBefore, TemplateID: f.TemplateID, BroadcastID: f.BroadcastID, RowLimit: rowLimit,
			})
		}
		return err
	})
	if err != nil {
		if ctx.Err() == nil && (qctx.Err() != nil || store.StatementTimedOut(err)) {
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

// uuidV7Floor is the smallest UUIDv7 with the millisecond of t. Email ids
// are generated by the database while inserting, after the transaction start
// that sets created_at, so an email created at or after t never has a smaller
// id — provided the database clock does not step backwards within a send
// transaction and created_at is never rewritten (no code does).
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
