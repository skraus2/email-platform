package automations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
)

// Errors of event reports callers branch on.
var (
	// ErrContactNotFound: no such contact in the project (also for
	// contacts of other projects).
	ErrContactNotFound = errors.New("contact not found")
	// ErrKeyReused: the idempotency key was used with a different payload.
	ErrKeyReused = emails.ErrIdempotencyKeyReused
)

// RateLimitError means the project reported as many events as it may in the
// current minute.
type RateLimitError struct {
	RetryAfter time.Duration // until the current window ends, 1s–60s
}

func (e *RateLimitError) Error() string { return "contact event rate limit exceeded" }

// DefaultEventsPerMinute caps reported events per project and minute unless
// NewEvents gets another value.
const DefaultEventsPerMinute = 1200

// EventInput is a reported contact event. Exactly one of ContactID and
// Email identifies the contact.
type EventInput struct {
	Name      string
	ContactID uuid.UUID
	Email     string
	// Data is a flat JSON object (contact attribute rules), empty for none.
	Data           []byte
	IdempotencyKey string
}

// RunRef names a run an event started.
type RunRef struct {
	ID           uuid.UUID
	AutomationID uuid.UUID
}

// Event is the result of a report.
type Event struct {
	ID        uuid.UUID
	Name      string
	ContactID uuid.UUID
	CreatedAt time.Time
	Runs      []RunRef
	// Replayed: the response of an earlier request with the same key.
	Replayed bool
}

// StepEnqueuer inserts automation step jobs inside the caller's transaction.
type StepEnqueuer interface {
	EnqueueAutomationStep(ctx context.Context, tx pgx.Tx, args jobs.AutomationStepArgs, at time.Time) error
}

// Events records contact events and starts the runs they trigger.
type Events struct {
	pool      *pgxpool.Pool
	enq       StepEnqueuer
	logger    *slog.Logger
	perMinute int
}

// NewEvents allows perMinute events per project and minute.
func NewEvents(pool *pgxpool.Pool, enq StepEnqueuer, logger *slog.Logger, perMinute int) *Events {
	return &Events{pool: pool, enq: enq, logger: logger, perMinute: max(perMinute, 1)}
}

// validate checks the input and returns the canonical data (nil when none).
func (in EventInput) validate() ([]byte, error) {
	if !ValidEventName(in.Name) {
		return nil, invalid("name", "must match ^[a-z0-9][a-z0-9_.:-]{0,99}$")
	}
	switch {
	case (in.ContactID == uuid.Nil) == (in.Email == ""):
		return nil, invalid("contact_id", "exactly one of contact_id and email is required")
	case in.Email != "" && emails.ValidateAddress(in.Email) != nil:
		return nil, invalid("email", "must be a single plain ASCII email address")
	}
	if in.IdempotencyKey != "" {
		if err := emails.ValidateIdempotencyKey(in.IdempotencyKey); err != nil {
			return nil, err
		}
	}
	if len(bytes.TrimSpace(in.Data)) == 0 {
		return nil, nil
	}
	data, err := contacts.ValidateAttributes(in.Data)
	if err != nil {
		var ce *contacts.Error
		if errors.As(err, &ce) {
			return nil, invalid("data"+strings.TrimPrefix(ce.Field, "attributes"), "%s", ce.Reason)
		}
		return nil, err
	}
	if string(data) == "{}" {
		return nil, nil
	}
	return data, nil
}

// fingerprint hashes what makes two requests the same: name, the contact
// reference as sent (id, or the address in lower case) and the canonical
// data, each length-prefixed.
func (in EventInput) fingerprint(data []byte) []byte {
	h := sha256.New()
	field := func(b []byte) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(b)))
		h.Write(n[:])
		h.Write(b)
	}
	field([]byte("event-v1"))
	field([]byte(in.Name))
	if in.ContactID != uuid.Nil {
		field([]byte("id"))
		field(in.ContactID[:])
	} else {
		field([]byte("email"))
		field([]byte(strings.ToLower(in.Email)))
	}
	field(data)
	return h.Sum(nil)
}

var errKeyTaken = errors.New("idempotency key taken concurrently")

// Report records an event for a contact of the project and starts a run of
// every active automation triggered by its name, subject to the re-entry
// rules; the runs' first step jobs are enqueued in the same transaction.
// Validation errors are *Error.
func (e *Events) Report(ctx context.Context, projectID uuid.UUID, in EventInput) (Event, error) {
	data, err := in.validate()
	if err != nil {
		return Event{}, err
	}
	var fingerprint []byte
	if in.IdempotencyKey != "" {
		fingerprint = in.fingerprint(data)
		if ev, found, err := e.match(ctx, store.New(e.pool), projectID, in.IdempotencyKey, fingerprint); err != nil || found {
			return ev, err
		}
	}
	// Read-only pre-check: a project over its limit does no write work and
	// does not queue on its window row. The count below stays authoritative.
	if c, err := store.New(e.pool).GetContactEventCount(ctx, projectID); err != nil {
		return Event{}, fmt.Errorf("event count: %w", err)
	} else if int(c.Count) >= e.perMinute {
		return Event{}, rateLimited(c.RetryAfterSeconds)
	}

	var ev Event
	err = lockedTxFunc(ctx, e.pool, func(tx pgx.Tx, q *store.Queries) error {
		params := store.LockContactForEventParams{ProjectID: projectID}
		if in.ContactID != uuid.Nil {
			params.ContactID = &in.ContactID
		} else {
			params.Email = &in.Email
		}
		contactID, err := q.LockContactForEvent(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrContactNotFound
		}
		if err != nil {
			return fmt.Errorf("lock contact: %w", err)
		}
		var key *string
		if in.IdempotencyKey != "" {
			key = &in.IdempotencyKey
		}
		row, err := q.InsertContactEvent(ctx, store.InsertContactEventParams{
			ProjectID: projectID, ContactID: contactID, Name: in.Name, IdempotencyKey: key, Fingerprint: fingerprint,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// A concurrent request with the same key committed first.
			return errKeyTaken
		}
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
		ev = Event{ID: row.ID, Name: in.Name, ContactID: contactID, CreatedAt: row.CreatedAt, Runs: []RunRef{}}

		autos, err := q.TriggeredAutomations(ctx, store.TriggeredAutomationsParams{ProjectID: projectID, TriggerEvent: in.Name})
		if err != nil {
			return fmt.Errorf("triggered automations: %w", err)
		}
		for _, a := range autos {
			blocked, err := q.BlockingRunExists(ctx, store.BlockingRunExistsParams{
				AutomationID: a.ID, ContactID: contactID, ProjectID: projectID, AllowReentry: a.AllowReentry,
			})
			if err != nil {
				return fmt.Errorf("re-entry check: %w", err)
			}
			if blocked {
				continue
			}
			run, err := q.InsertRun(ctx, store.InsertRunParams{
				ProjectID: projectID, AutomationID: a.ID, ContactID: contactID, TriggerEventID: &row.ID, TriggerData: data,
			})
			if err != nil {
				return fmt.Errorf("insert run: %w", err)
			}
			if err := e.enq.EnqueueAutomationStep(ctx, tx, jobs.AutomationStepArgs{RunID: run.ID, ProjectID: projectID, Step: 0}, time.Time{}); err != nil {
				return err
			}
			ev.Runs = append(ev.Runs, RunRef{ID: run.ID, AutomationID: a.ID})
		}
		// Counted last, so the project's window row is locked only briefly.
		c, err := q.CountContactEvent(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count event: %w", err)
		}
		if int(c.Count) > e.perMinute {
			return rateLimited(c.RetryAfterSeconds)
		}
		return nil
	})
	if errors.Is(err, ErrContactNotFound) {
		// Counted although refused: otherwise a key could test addresses
		// for membership of the contact list without any limit.
		if _, cerr := store.New(e.pool).CountContactEvent(ctx, projectID); cerr != nil {
			return Event{}, fmt.Errorf("count refused event: %w", cerr)
		}
		return Event{}, err
	}
	if errors.Is(err, errKeyTaken) {
		// Rolled back; the winner's event is committed and visible now.
		got, found, err := e.match(ctx, store.New(e.pool), projectID, in.IdempotencyKey, fingerprint)
		if err == nil && !found {
			err = errors.New("idempotency key vanished after a conflict")
		}
		return got, err
	}
	if err != nil {
		return Event{}, err
	}
	if len(ev.Runs) > 0 {
		e.logger.InfoContext(ctx, "automation runs started",
			slog.String("project_id", projectID.String()), slog.String("event_id", ev.ID.String()), slog.Int("runs", len(ev.Runs)))
	}
	return ev, nil
}

// match looks up an event by idempotency key: found with the same
// fingerprint gives the replay, with another fingerprint ErrKeyReused.
func (e *Events) match(ctx context.Context, q *store.Queries, projectID uuid.UUID, key string, fingerprint []byte) (Event, bool, error) {
	k := key
	row, err := q.GetContactEventByKey(ctx, store.GetContactEventByKeyParams{ProjectID: projectID, IdempotencyKey: &k})
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, fmt.Errorf("look up idempotency key: %w", err)
	}
	if !bytes.Equal(row.Fingerprint, fingerprint) {
		return Event{}, false, ErrKeyReused
	}
	runs, err := q.RunsOfEvent(ctx, store.RunsOfEventParams{TriggerEventID: &row.ID, ProjectID: projectID})
	if err != nil {
		return Event{}, false, fmt.Errorf("runs of event: %w", err)
	}
	ev := Event{ID: row.ID, Name: row.Name, ContactID: row.ContactID, CreatedAt: row.CreatedAt, Runs: make([]RunRef, 0, len(runs)), Replayed: true}
	for _, r := range runs {
		ev.Runs = append(ev.Runs, RunRef{ID: r.ID, AutomationID: r.AutomationID})
	}
	return ev, true, nil
}

func rateLimited(retryAfterSeconds int32) error {
	return &RateLimitError{RetryAfter: time.Duration(min(max(retryAfterSeconds, 1), 60)) * time.Second}
}

// DeleteOldEventWindows removes rate limit windows that ended long ago (run
// from the periodic cleanup).
func DeleteOldEventWindows(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := store.New(pool).DeleteContactEventWindowsBefore(ctx); err != nil {
		return fmt.Errorf("cleanup contact event windows: %w", err)
	}
	return nil
}
