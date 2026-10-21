// Package events keeps the immutable history of an email (platform
// transitions and provider events) and projects provider events onto the
// email status.
//
// Provider events can arrive twice, late and out of order. They are stored
// once (deduplicated on the provider's event id) and move a dispatched email
// only upwards in rank: submitted < delivered < bounced < complained. So the
// final status is the same for every arrival order, and a late delivery never
// hides a bounce or complaint. Only emails sent through a real provider can
// reach those statuses; the database refuses them for the local test sink.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

// Change is a status change of an email, caused by the event EventID.
type Change struct {
	ProjectID uuid.UUID
	EmailID   uuid.UUID
	EventID   uuid.UUID
	Status    string
}

// Hook runs inside the transaction that changed the status, exactly once per
// change. An error rolls the change back.
type Hook func(ctx context.Context, tx pgx.Tx, c Change) error

// Recorder writes events and applies the projection.
type Recorder struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	hook   Hook
	// afterInsert runs after a provider event is inserted, before commit
	// (tests only).
	afterInsert func()
}

// NewRecorder builds a Recorder without a status-change hook.
func NewRecorder(pool *pgxpool.Pool, logger *slog.Logger) *Recorder {
	return &Recorder{pool: pool, logger: logger}
}

// OnStatusChange sets the hook called for every status change. Set it before
// the recorder is used.
func (r *Recorder) OnStatusChange(h Hook) { r.hook = h }

func (r *Recorder) changed(ctx context.Context, tx pgx.Tx, c Change) error {
	if r.hook == nil {
		return nil
	}
	return r.hook(ctx, tx, c)
}

// Platform event types: the email's own transitions.
const (
	TypeCaptured  = "captured"
	TypeSubmitted = "submitted"
	TypeFailed    = "failed"
	TypeCanceled  = "canceled"
)

// Transition records the platform event for a status change the caller just
// made in tx. Call it if and only if the status update affected a row.
// provider may be empty; errorCode is set for failures only.
func (r *Recorder) Transition(ctx context.Context, tx pgx.Tx, projectID, emailID uuid.UUID, status, provider, errorCode string) error {
	details := map[string]string{}
	if errorCode != "" {
		details["error_code"] = errorCode
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode event details: %w", err)
	}
	var prov *string
	if provider != "" {
		prov = &provider
	}
	id, err := store.New(tx).InsertPlatformEvent(ctx, store.InsertPlatformEventParams{
		ProjectID: projectID, EmailID: emailID, Type: status, Provider: prov, Details: raw,
	})
	if err != nil {
		return fmt.Errorf("insert %s event: %w", status, err)
	}
	return r.changed(ctx, tx, Change{ProjectID: projectID, EmailID: emailID, EventID: id, Status: status})
}

// Dispatched records the provider acceptance the caller just wrote in tx
// (captured or submitted). For a submitted email it then applies provider
// events that arrived while it was still being sent. That runs as its own
// statement after the status update, so it sees every event committed by an
// ingest that held the row lock before.
func (r *Recorder) Dispatched(ctx context.Context, tx pgx.Tx, projectID, emailID uuid.UUID, status, provider string) error {
	if err := r.Transition(ctx, tx, projectID, emailID, status, provider, ""); err != nil {
		return err
	}
	// Only events of the provider that sent the email can be projected
	// (the database refuses provider statuses for any other).
	if status != TypeSubmitted || provider != "ses" {
		return nil
	}
	q := store.New(tx)
	top, err := q.TopProviderEvent(ctx, store.TopProviderEventParams{ProjectID: projectID, EmailID: emailID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load stored provider events: %w", err)
	}
	n, err := q.ProjectEmailStatus(ctx, store.ProjectEmailStatusParams{ID: emailID, ProjectID: projectID, Status: top.Type})
	if err != nil {
		return fmt.Errorf("project stored events: %w", err)
	}
	if n == 0 {
		return nil
	}
	return r.changed(ctx, tx, Change{ProjectID: projectID, EmailID: emailID, EventID: top.ID, Status: top.Type})
}

// ProviderEvent is an authenticated, parsed event of a real provider.
type ProviderEvent struct {
	Provider          string // "ses"
	ProviderEventID   string // dedup key, unique per provider
	ProviderMessageID string // the provider's message id, if known
	EmailID           uuid.UUID
	ProjectID         uuid.UUID
	Type              string // one of the provider event types
	OccurredAt        time.Time
	// Details holds only non-personal values under allowlisted keys.
	Details map[string]string
	// Recipients are the addresses a complaint or permanent bounce names.
	// They are used only to suppress them when the email itself no longer
	// exists; never stored with the event, never logged.
	Recipients []string
	// RecipientsDropped counts named addresses that were unusable or over
	// the limit (logged as a number only).
	RecipientsDropped int
}

// Outcome of an ingest. Every outcome except an error is final: the event
// will never be processed differently on a retry.
type Outcome int

// Ingest outcomes.
const (
	Stored           Outcome = iota + 1 // new event stored (status possibly changed)
	Duplicate                           // stored before; nothing changed
	UnknownEmail                        // no such email in that project; nothing stored
	ProviderMismatch                    // the email went to another provider; nothing stored
	// UnknownEmailSuppressed: no such email (e.g. deleted by retention), but
	// the complaint or permanent bounce suppressed the addresses it names.
	UnknownEmailSuppressed
)

func (o Outcome) String() string {
	switch o {
	case Stored:
		return "stored"
	case Duplicate:
		return "duplicate"
	case UnknownEmail:
		return "unknown_email"
	case ProviderMismatch:
		return "provider_mismatch"
	case UnknownEmailSuppressed:
		return "unknown_email_suppressed"
	}
	return "invalid"
}

// ErrInvalidEvent means the event cannot be stored as given.
var ErrInvalidEvent = errors.New("invalid provider event")

var providerTypes = map[string]bool{
	"send": true, "delivered": true, "bounced": true, "complained": true,
	"rejected": true, "delivery_delayed": true,
}

// projected are the provider event types that move the status.
var projected = map[string]bool{"delivered": true, "bounced": true, "complained": true}

var detailKeys = map[string]bool{
	"bounce_type": true, "bounce_subtype": true, "complaint_feedback_type": true, "delay_type": true,
}

const (
	maxIDLen     = 512
	maxDetailLen = 64
	// maxClockSkew is how far a provider's event time may lie ahead of ours.
	maxClockSkew = time.Hour
)

func (e ProviderEvent) validate() error {
	switch {
	case e.Provider != "ses":
		return fmt.Errorf("%w: unknown provider", ErrInvalidEvent)
	case !providerTypes[e.Type]:
		return fmt.Errorf("%w: unknown type", ErrInvalidEvent)
	case e.ProviderEventID == "" || len(e.ProviderEventID) > maxIDLen:
		return fmt.Errorf("%w: event id must be 1 to %d bytes", ErrInvalidEvent, maxIDLen)
	case len(e.ProviderMessageID) > maxIDLen:
		return fmt.Errorf("%w: message id longer than %d bytes", ErrInvalidEvent, maxIDLen)
	case e.OccurredAt.IsZero():
		return fmt.Errorf("%w: no event time", ErrInvalidEvent)
	case e.OccurredAt.After(time.Now().Add(maxClockSkew)):
		return fmt.Errorf("%w: event time in the future", ErrInvalidEvent)
	case e.EmailID == uuid.Nil || e.ProjectID == uuid.Nil:
		return fmt.Errorf("%w: email or project id missing", ErrInvalidEvent)
	}
	for k, v := range e.Details {
		if !detailKeys[k] {
			return fmt.Errorf("%w: detail key not allowed", ErrInvalidEvent)
		}
		if len(v) == 0 || len(v) > maxDetailLen || !printable(v) {
			return fmt.Errorf("%w: detail %s must be 1 to %d printable characters", ErrInvalidEvent, k, maxDetailLen)
		}
	}
	return nil
}

func printable(v string) bool {
	for i := range len(v) {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}

// suppressionReason returns why a stored event suppresses the recipient:
// any complaint, and bounces the provider calls permanent.
func suppressionReason(e ProviderEvent) string {
	switch {
	case e.Type == "complained":
		return "complaint"
	case e.Type == "bounced" && e.Details["bounce_type"] == "Permanent":
		return "bounce"
	}
	return ""
}

// suppressUnknown handles a provider event whose email does not exist (any
// more): a complaint or permanent bounce still suppresses the addresses the
// provider names, in the event's project, so a deleted email cannot let its
// recipient be mailed again. Nothing else is stored.
// It returns the outcome, a warning for a suppression that could not be
// made, and how many addresses were newly suppressed.
func suppressUnknown(ctx context.Context, q *store.Queries, e ProviderEvent) (Outcome, string, int64, error) {
	reason := suppressionReason(e)
	if reason == "" {
		return UnknownEmail, "", 0, nil
	}
	if len(e.Recipients) == 0 {
		return UnknownEmail, "suppressing event without usable recipient", 0, nil
	}
	// The suppression's project must exist: an insert for a missing one
	// would fail on every redelivery.
	exists, err := q.ProjectExists(ctx, e.ProjectID)
	if err != nil {
		return 0, "", 0, fmt.Errorf("check project: %w", err)
	}
	if !exists {
		return UnknownEmail, "suppressing event for unknown project", 0, nil
	}
	var added int64
	for _, addr := range e.Recipients {
		n, err := q.AddSuppression(ctx, store.AddSuppressionParams{ProjectID: e.ProjectID, Recipient: addr, Reason: reason})
		if err != nil {
			return 0, "", 0, fmt.Errorf("suppress recipient: %w", err)
		}
		added += n
	}
	return UnknownEmailSuppressed, "", added, nil
}

// Ingest stores a provider event in its own transaction, suppresses the
// recipient for a permanent bounce or a complaint, and projects the event
// onto the email status.
func (r *Recorder) Ingest(ctx context.Context, e ProviderEvent) (Outcome, error) {
	if err := e.validate(); err != nil {
		return 0, err
	}
	log := r.logger.With(
		slog.String("project_id", e.ProjectID.String()),
		slog.String("email_id", e.EmailID.String()),
		slog.String("event_type", e.Type),
	)
	var out Outcome
	var warn string
	var added int64
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		out, warn, added = 0, "", 0
		q := store.New(tx)
		// Lock the email first: the worker's finish takes the same row lock,
		// so either it sees this event or this ingest sees its status.
		email, err := q.LockEmailForEvent(ctx, store.LockEmailForEventParams{ID: e.EmailID, ProjectID: e.ProjectID})
		if errors.Is(err, pgx.ErrNoRows) {
			out, warn, added, err = suppressUnknown(ctx, q, e)
			return err
		}
		if err != nil {
			return fmt.Errorf("lock email: %w", err)
		}
		// A NULL provider means not dispatched yet: store and wait.
		if email.Provider != nil && *email.Provider != e.Provider {
			out = ProviderMismatch
			return nil
		}
		details, err := json.Marshal(nonNil(e.Details))
		if err != nil {
			return fmt.Errorf("encode event details: %w", err)
		}
		var msgID *string
		if e.ProviderMessageID != "" {
			msgID = &e.ProviderMessageID
		}
		eventID, err := q.InsertProviderEvent(ctx, store.InsertProviderEventParams{
			ProjectID: e.ProjectID, EmailID: e.EmailID, Origin: e.Provider, Type: e.Type,
			ProviderEventID: &e.ProviderEventID, ProviderMessageID: msgID,
			OccurredAt: e.OccurredAt, Details: details,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			out = Duplicate
			owner, err := q.ProviderEventOwner(ctx, store.ProviderEventOwnerParams{Provider: &e.Provider, ProviderEventID: &e.ProviderEventID})
			if err != nil {
				return fmt.Errorf("load duplicate event: %w", err)
			}
			if owner.ProjectID != e.ProjectID || owner.EmailID != e.EmailID {
				// The provider reused an event id for another email (or a
				// parser derived it wrongly): it must not vanish silently.
				warn = "provider event id already used by another email"
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
		if r.afterInsert != nil {
			r.afterInsert()
		}
		out = Stored
		if msgID != nil && email.ProviderID != nil && *email.ProviderID != *msgID {
			warn = "possible_duplicate_send"
		}
		if reason := suppressionReason(e); reason != "" {
			if _, err := q.AddSuppression(ctx, store.AddSuppressionParams{
				ProjectID: e.ProjectID, Recipient: email.Recipient, Reason: reason,
			}); err != nil {
				return fmt.Errorf("suppress recipient: %w", err)
			}
		}
		if email.Status == "failed" {
			warn = "provider event for a failed email"
		}
		if !projected[e.Type] {
			return nil
		}
		n, err := q.ProjectEmailStatus(ctx, store.ProjectEmailStatusParams{ID: e.EmailID, ProjectID: e.ProjectID, Status: e.Type})
		if err != nil {
			return fmt.Errorf("project event: %w", err)
		}
		if n == 0 {
			return nil
		}
		return r.changed(ctx, tx, Change{ProjectID: e.ProjectID, EmailID: e.EmailID, EventID: eventID, Status: e.Type})
	})
	if err != nil {
		return 0, fmt.Errorf("ingest event: %w", err)
	}
	attrs := []any{slog.String("outcome", out.String())}
	if out == UnknownEmailSuppressed {
		attrs = append(attrs, slog.Int64("suppressed", added))
	}
	if e.RecipientsDropped > 0 {
		attrs = append(attrs, slog.Int("recipients_dropped", e.RecipientsDropped))
	}
	if warn != "" {
		// The recipient is never logged unmasked; the email id identifies it.
		log.WarnContext(ctx, warn, attrs...)
	} else {
		log.InfoContext(ctx, "provider event", attrs...)
	}
	return out, nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// Event is a stored event as clients see it.
type Event struct {
	ID         uuid.UUID
	Type       string
	Origin     string // platform or the provider
	Provider   *string
	OccurredAt time.Time
	ReceivedAt time.Time
	Details    map[string]string
}

// List returns up to limit events of an email received after the event
// after (uuid.Nil: from the start), oldest first. The caller has checked
// that the email belongs to the project.
func List(ctx context.Context, q *store.Queries, projectID, emailID, after uuid.UUID, limit int) ([]Event, error) {
	rows, err := q.ListEmailEvents(ctx, store.ListEmailEventsParams{
		ProjectID: projectID, EmailID: emailID, After: after, Limit: int32(limit), //nolint:gosec // callers bound limit
	})
	if err != nil {
		return nil, fmt.Errorf("list email events: %w", err)
	}
	out := make([]Event, 0, len(rows))
	for _, r := range rows {
		stored := map[string]string{}
		if err := json.Unmarshal(r.Details, &stored); err != nil {
			return nil, fmt.Errorf("decode event details: %w", err)
		}
		// Filtered on read too, so a write path that skipped validation can
		// never expose more than the allowlist.
		details := map[string]string{}
		for k, v := range stored {
			if (detailKeys[k] || k == "error_code") && len(v) <= maxDetailLen && printable(v) {
				details[k] = v
			}
		}
		out = append(out, Event{
			ID: r.ID, Type: r.Type, Origin: r.Origin, Provider: r.Provider,
			OccurredAt: r.OccurredAt, ReceivedAt: r.ReceivedAt, Details: details,
		})
	}
	return out, nil
}
