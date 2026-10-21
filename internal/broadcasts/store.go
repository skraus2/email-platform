// Package broadcasts sends one template version to the subscribed members of
// an audience, one email per recipient. A broadcast starts as a draft, is
// scheduled (now or later), fans out into recipient emails in batches and
// ends completed (every recipient email exists) or canceled.
package broadcasts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
)

// Statuses.
const (
	StatusDraft     = "draft"
	StatusScheduled = "scheduled"
	StatusSending   = "sending"
	StatusCompleted = "completed"
	StatusCanceled  = "canceled"
)

// Errors callers branch on.
var (
	// ErrNotFound: no such broadcast in the project (also for broadcasts of
	// other projects).
	ErrNotFound           = errors.New("broadcast not found")
	ErrAudienceNotFound   = errors.New("audience not found")
	ErrTemplateNotFound   = errors.New("template not found")
	ErrMissingUnsubscribe = errors.New("template version does not print the unsubscribe link")
	ErrLimit              = errors.New("open broadcast limit reached")
	ErrNotDraft           = errors.New("broadcast is not a draft")
	ErrNotCancelable      = errors.New("broadcast is already canceled")
	// ErrBusy: a lock wait ran out; retry later.
	ErrBusy = errors.New("broadcasts busy")
)

// Limits.
const (
	MaxNameLength      = 200
	MaxURLTemplate     = 900
	MaxSharedVariables = 16 << 10
	// TokenPlaceholder is replaced by the contact's unsubscribe token.
	TokenPlaceholder = "{token}"
	// DefaultScheduleMaxAhead bounds how far ahead a broadcast may start.
	DefaultScheduleMaxAhead = 720 * time.Hour
	// MaxPage bounds one list page.
	MaxPage = 100
)

// Error is a client-facing validation error; Reason never repeats a
// submitted value.
type Error struct {
	Field  string
	Reason string
}

func (e *Error) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, format string, args ...any) error {
	return &Error{Field: field, Reason: fmt.Sprintf(format, args...)}
}

// EmailStatuses are the statuses counted per broadcast.
var EmailStatuses = []string{"queued", "sending", "captured", "submitted", "delivered", "bounced", "complained", "failed", "canceled"}

// Counts is the progress of a broadcast.
type Counts struct {
	Recipients          int
	SkippedUnsubscribed int
	SkippedSuppressed   int
	SkippedInvalid      int
	// ByStatus has every status of EmailStatuses (zero when none).
	ByStatus map[string]int
}

// Broadcast is the client-visible state of a broadcast. Shared variables
// are not part of it.
type Broadcast struct {
	ID              uuid.UUID
	ProjectID       uuid.UUID
	Name            string
	Status          string
	AudienceID      uuid.UUID
	From            string
	TemplateID      uuid.UUID
	TemplateVersion int
	UnsubscribeURL  string
	ScheduledAt     *time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time
	// Counts is set by Get only.
	Counts *Counts
}

func fromRow(r store.Broadcast) Broadcast {
	return Broadcast{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Status: r.Status, AudienceID: r.AudienceID, From: r.Sender,
		TemplateID: r.TemplateID, TemplateVersion: int(r.TemplateVersion), UnsubscribeURL: r.UnsubscribeUrl,
		ScheduledAt: r.ScheduledAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, CreatedAt: r.CreatedAt,
	}
}

// CreateInput is a new draft. TemplateVersion 0 means the highest version
// at create time.
type CreateInput struct {
	Name            string
	AudienceID      uuid.UUID
	From            string
	TemplateID      uuid.UUID
	TemplateVersion int
	Variables       json.RawMessage
	UnsubscribeURL  string
}

// Enqueuer inserts the fan-out job inside the caller's transaction.
type Enqueuer interface {
	EnqueueBroadcastFanout(ctx context.Context, tx pgx.Tx, args jobs.BroadcastFanoutArgs, at time.Time) error
}

// Store persists broadcasts.
type Store struct {
	pool          *pgxpool.Pool
	enq           Enqueuer
	maxOpen       int
	requireSender bool
	scheduleAhead time.Duration
}

// NewStore caps the broadcasts of a project in draft, scheduled or sending
// at maxOpen.
func NewStore(pool *pgxpool.Pool, enq Enqueuer, maxOpen int) *Store {
	return &Store{pool: pool, enq: enq, maxOpen: max(maxOpen, 1), scheduleAhead: DefaultScheduleMaxAhead}
}

// RequireVerifiedSenders makes Create and Schedule refuse a from address
// without a verified sender identity of the project.
func (s *Store) RequireVerifiedSenders() { s.requireSender = true }

// LimitScheduleAhead sets how far ahead a broadcast may start (at most
// DefaultScheduleMaxAhead).
func (s *Store) LimitScheduleAhead(d time.Duration) {
	s.scheduleAhead = min(max(d, time.Minute), DefaultScheduleMaxAhead)
}

// ValidateURLTemplate checks an unsubscribe URL with {token} exactly once:
// with a token in place it must be a valid List-Unsubscribe URL.
func ValidateURLTemplate(u string) error {
	switch {
	case len(u) > MaxURLTemplate:
		return invalid("unsubscribe_url", "must be at most %d characters", MaxURLTemplate)
	case strings.Count(u, TokenPlaceholder) != 1:
		return invalid("unsubscribe_url", "must contain {token} exactly once")
	}
	sample := strings.Replace(u, TokenPlaceholder, strings.Repeat("A", 43), 1)
	if err := delivery.ValidateListUnsubscribeURL(sample); err != nil {
		return invalid("unsubscribe_url", "must be an absolute https URL without user information, spaces, commas, quotes or angle brackets")
	}
	return nil
}

func validateName(name string) error {
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxNameLength {
		return invalid("name", "must be valid UTF-8 of at most %d characters", MaxNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Zl, unicode.Zp) {
			return invalid("name", "must not contain control or text-direction characters")
		}
	}
	return nil
}

// sharedVariables checks the shared values against the version's schema and
// returns them as compact JSON.
func sharedVariables(schema templates.Schema, raw json.RawMessage) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte(`{}`), nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, invalid("variables", "must be a JSON object")
	}
	if b.Len() > MaxSharedVariables {
		return nil, invalid("variables", "must be at most %d bytes as compact JSON", MaxSharedVariables)
	}
	var names map[string]json.RawMessage
	if err := json.Unmarshal(b.Bytes(), &names); err != nil {
		return nil, invalid("variables", "must be a JSON object")
	}
	if _, ok := names[contacts.ReservedAttribute]; ok {
		return nil, invalid("variables", "must not set unsubscribe_url (the platform fills it in)")
	}
	if err := templates.ValidateSharedVariables(schema, b.Bytes()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// checkVersion requires the version to declare unsubscribe_url as a string
// and to print it outside conditions in its text and (if any) HTML part.
func checkVersion(v templates.Version) error {
	c, err := v.Compile()
	if err != nil {
		return fmt.Errorf("compile template version: %w", err)
	}
	spec, ok := c.Schema()[contacts.ReservedAttribute]
	if !ok || spec.Type != templates.TypeString || !c.PrintsTopLevel("text", contacts.ReservedAttribute) ||
		(c.HasHTML() && !c.PrintsTopLevel("html", contacts.ReservedAttribute)) {
		return ErrMissingUnsubscribe
	}
	return nil
}

// Create validates the input and stores a draft.
func (s *Store) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (Broadcast, error) {
	if err := emails.ValidateAddress(in.From); err != nil {
		return Broadcast{}, invalid("from", "must be a single plain ASCII email address")
	}
	if err := validateName(in.Name); err != nil {
		return Broadcast{}, err
	}
	if err := ValidateURLTemplate(in.UnsubscribeURL); err != nil {
		return Broadcast{}, err
	}
	q := store.New(s.pool)
	if _, err := q.GetAudience(ctx, store.GetAudienceParams{ID: in.AudienceID, ProjectID: projectID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Broadcast{}, ErrAudienceNotFound
		}
		return Broadcast{}, fmt.Errorf("get audience: %w", err)
	}
	v, err := templates.Resolve(ctx, q, projectID, in.TemplateID, in.TemplateVersion)
	if errors.Is(err, templates.ErrNotFound) {
		return Broadcast{}, ErrTemplateNotFound
	}
	if err != nil {
		return Broadcast{}, fmt.Errorf("resolve template: %w", err)
	}
	if err := checkVersion(v); err != nil {
		return Broadcast{}, err
	}
	schema, err := templates.ParseSchema(v.Schema)
	if err != nil {
		return Broadcast{}, fmt.Errorf("stored schema: %w", err)
	}
	vars, err := sharedVariables(schema, in.Variables)
	if err != nil {
		return Broadcast{}, err
	}
	if err := s.checkSender(ctx, q, projectID, in.From); err != nil {
		return Broadcast{}, err
	}

	var row store.Broadcast
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetContactLockTimeout(ctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		if _, err := q.LockProjectForBroadcasts(ctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock project: %w", err)
		}
		n, err := q.CountOpenBroadcasts(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count broadcasts: %w", err)
		}
		if n >= int64(s.maxOpen) {
			return ErrLimit
		}
		row, err = q.InsertBroadcast(ctx, store.InsertBroadcastParams{
			ProjectID: projectID, Name: in.Name, AudienceID: in.AudienceID, Sender: in.From,
			TemplateID: v.TemplateID, TemplateVersion: int32(v.Version), Variables: vars, //nolint:gosec // versions are capped far below 2^31
			UnsubscribeUrl: in.UnsubscribeURL,
		})
		if err != nil {
			return fmt.Errorf("insert broadcast: %w", err)
		}
		return nil
	})
	if err != nil {
		return Broadcast{}, busy(err)
	}
	return fromRow(row), nil
}

func (s *Store) checkSender(ctx context.Context, q *store.Queries, projectID uuid.UUID, from string) error {
	if !s.requireSender {
		return nil
	}
	ok, err := emails.SenderVerified(ctx, q, projectID, from)
	if err != nil {
		return err
	}
	if !ok {
		return emails.ErrSenderNotVerified
	}
	return nil
}

// Get returns a broadcast of the project with its counts.
func (s *Store) Get(ctx context.Context, projectID, id uuid.UUID) (Broadcast, error) {
	q := store.New(s.pool)
	row, err := q.GetBroadcast(ctx, store.GetBroadcastParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Broadcast{}, ErrNotFound
	}
	if err != nil {
		return Broadcast{}, fmt.Errorf("get broadcast: %w", err)
	}
	rows, err := q.CountBroadcastEmails(ctx, store.CountBroadcastEmailsParams{BroadcastID: &id, ProjectID: projectID})
	if err != nil {
		return Broadcast{}, fmt.Errorf("count broadcast emails: %w", err)
	}
	c := &Counts{
		Recipients: int(row.Recipients), SkippedUnsubscribed: int(row.SkippedUnsubscribed),
		SkippedSuppressed: int(row.SkippedSuppressed), SkippedInvalid: int(row.SkippedInvalid),
		ByStatus: make(map[string]int, len(EmailStatuses)),
	}
	for _, st := range EmailStatuses {
		c.ByStatus[st] = 0
	}
	for _, r := range rows {
		c.ByStatus[r.Status] = int(r.N)
	}
	b := fromRow(row)
	b.Counts = c
	return b, nil
}

// List returns up to limit broadcasts of the project, newest first, after
// the cursor (uuid.Nil: from the newest), optionally of one status.
func (s *Store) List(ctx context.Context, projectID uuid.UUID, status string, after uuid.UUID, limit int) ([]Broadcast, bool, error) {
	limit = min(max(limit, 1), MaxPage)
	before := after
	if before == uuid.Nil {
		before = uuid.Max
	}
	var st *string
	if status != "" {
		st = &status
	}
	rows, err := store.New(s.pool).ListBroadcasts(ctx, store.ListBroadcastsParams{
		ProjectID: projectID, BeforeID: before, Status: st, RowLimit: int32(limit + 1), //nolint:gosec // limit ≤ MaxPage
	})
	if err != nil {
		return nil, false, fmt.Errorf("list broadcasts: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]Broadcast, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, more, nil
}

// Schedule moves a draft to scheduled and enqueues its fan-out for at
// (nil or not in the future: now).
func (s *Store) Schedule(ctx context.Context, projectID, id uuid.UUID, at *time.Time) (Broadcast, error) {
	now := time.Now()
	start := now
	if at != nil {
		if at.After(now.Add(s.scheduleAhead)) {
			return Broadcast{}, invalid("scheduled_at", "must be at most %s ahead", s.scheduleAhead)
		}
		if at.After(now) {
			start = *at
		}
	}
	start = start.UTC().Truncate(time.Microsecond)
	var row store.Broadcast
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetContactLockTimeout(ctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		b, err := q.LockBroadcast(ctx, store.LockBroadcastParams{ID: id, ProjectID: projectID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock broadcast: %w", err)
		}
		if b.Status != StatusDraft {
			return ErrNotDraft
		}
		if err := s.checkSender(ctx, q, projectID, b.Sender); err != nil {
			return err
		}
		row, err = q.ScheduleBroadcast(ctx, store.ScheduleBroadcastParams{ID: id, ProjectID: projectID, ScheduledAt: &start})
		if err != nil {
			return fmt.Errorf("schedule broadcast: %w", err)
		}
		return s.enq.EnqueueBroadcastFanout(ctx, tx, jobs.BroadcastFanoutArgs{BroadcastID: id, ProjectID: projectID}, start)
	})
	if err != nil {
		return Broadcast{}, busy(err)
	}
	return fromRow(row), nil
}

// Cancel moves a broadcast in any other status to canceled: no further
// recipient emails are created, and queued ones are not sent.
func (s *Store) Cancel(ctx context.Context, projectID, id uuid.UUID) (Broadcast, error) {
	q := store.New(s.pool)
	row, err := q.CancelBroadcast(ctx, store.CancelBroadcastParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.GetBroadcast(ctx, store.GetBroadcastParams{ID: id, ProjectID: projectID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Broadcast{}, ErrNotFound
			}
			return Broadcast{}, fmt.Errorf("get broadcast: %w", err)
		}
		return Broadcast{}, ErrNotCancelable
	}
	if err != nil {
		return Broadcast{}, fmt.Errorf("cancel broadcast: %w", err)
	}
	return fromRow(row), nil
}

// IsStatus reports whether st is a broadcast status.
func IsStatus(st string) bool {
	switch st {
	case StatusDraft, StatusScheduled, StatusSending, StatusCompleted, StatusCanceled:
		return true
	}
	return false
}

func busy(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "57014") {
		return ErrBusy
	}
	return err
}
