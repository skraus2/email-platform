// Package contacts keeps a project's contacts: an address with the person's
// consent status for broadcasts and where that consent came from, attributes
// for personalising broadcasts, and the token that identifies the contact in
// unsubscribe links (never shown by any API).
package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/store"
)

// Consent statuses.
const (
	StatusSubscribed   = "subscribed"
	StatusUnsubscribed = "unsubscribed"
)

// Errors callers branch on.
var (
	// ErrNotFound: no such contact in the project (also for contacts of
	// other projects).
	ErrNotFound = errors.New("contact not found")
	ErrExists   = errors.New("contact address already used in this project")
	ErrLimit    = errors.New("contact limit reached")
	// ErrBusy: a lock wait ran out; retry later.
	ErrBusy = errors.New("contacts busy")
)

// MaxPage bounds one list page.
const MaxPage = 100

// Contact is the client-visible state of a contact.
type Contact struct {
	ID              uuid.UUID
	ProjectID       uuid.UUID
	Email           string
	Status          string
	ConsentSource   *string
	StatusChangedAt time.Time
	// Attributes is compact JSON with sorted names.
	Attributes json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func fromRow(r store.Contact) (Contact, error) {
	attrs, err := fromStored(r.Attributes)
	if err != nil {
		return Contact{}, fmt.Errorf("stored attributes of contact %s: %w", r.ID, err)
	}
	return Contact{
		ID: r.ID, ProjectID: r.ProjectID, Email: r.Email, Status: r.Status, ConsentSource: r.ConsentSource,
		StatusChangedAt: r.StatusChangedAt, Attributes: attrs, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateInput is a new contact. ConsentSource is required when Status is
// subscribed; Attributes may be empty.
type CreateInput struct {
	Email         string
	Status        string
	ConsentSource *string
	Attributes    json.RawMessage
}

// UpdateInput changes a contact; nil fields stay unchanged, Attributes
// replaces all attributes.
type UpdateInput struct {
	Status        *string
	ConsentSource *string
	Attributes    json.RawMessage
}

// ListFilter narrows a list; zero fields match everything. After is the id
// of the last contact of the previous page.
type ListFilter struct {
	Email  string
	Status string
	After  uuid.UUID
}

// Store persists contacts.
type Store struct {
	pool          *pgxpool.Pool
	maxPerProject int
}

// NewStore caps the contacts of one project at maxPerProject.
func NewStore(pool *pgxpool.Pool, maxPerProject int) *Store {
	return &Store{pool: pool, maxPerProject: max(maxPerProject, 1)}
}

func validStatus(s string) bool { return s == StatusSubscribed || s == StatusUnsubscribed }

// validateConsent checks a status with the consent source given with it.
func validateConsent(status string, source *string) error {
	if !validStatus(status) {
		return invalid("status", "must be subscribed or unsubscribed")
	}
	if source != nil {
		if err := validateConsentSource(*source); err != nil {
			return err
		}
	}
	if status == StatusSubscribed && source == nil {
		return invalid("consent_source", "is required when the status is subscribed")
	}
	return nil
}

// Create adds a contact. Validation errors are *Error.
func (s *Store) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (Contact, error) {
	if err := emails.ValidateAddress(in.Email); err != nil {
		return Contact{}, invalid("email", "must be a single plain ASCII email address")
	}
	if err := validateConsent(in.Status, in.ConsentSource); err != nil {
		return Contact{}, err
	}
	attrs, err := ValidateAttributes(in.Attributes)
	if err != nil {
		return Contact{}, err
	}
	token, err := newToken()
	if err != nil {
		return Contact{}, err
	}
	var row store.Contact
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetContactLockTimeout(ctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		if _, err := q.LockProjectForContacts(ctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock project: %w", err)
		}
		n, err := q.CountContacts(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count contacts: %w", err)
		}
		if n >= int64(s.maxPerProject) {
			return ErrLimit
		}
		row, err = q.InsertContact(ctx, store.InsertContactParams{
			ProjectID: projectID, Email: in.Email, Status: in.Status, ConsentSource: in.ConsentSource,
			Attributes: attrs, UnsubscribeToken: token,
		})
		if isCode(err, "23505") {
			return ErrExists
		}
		if err != nil {
			return fmt.Errorf("insert contact: %w", err)
		}
		return nil
	})
	if err != nil {
		return Contact{}, busy(err)
	}
	return fromRow(row)
}

// Get returns a contact of the project.
func (s *Store) Get(ctx context.Context, projectID, id uuid.UUID) (Contact, error) {
	row, err := store.New(s.pool).GetContact(ctx, store.GetContactParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("get contact: %w", err)
	}
	return fromRow(row)
}

// Update changes a contact of the project. A status of subscribed needs a
// consent source in the same input. Validation errors are *Error.
func (s *Store) Update(ctx context.Context, projectID, id uuid.UUID, in UpdateInput) (Contact, error) {
	if in.Status == nil && in.ConsentSource == nil && in.Attributes == nil {
		return Contact{}, invalid("contact", "at least one of status, consent_source and attributes is required")
	}
	if in.Status != nil {
		if err := validateConsent(*in.Status, in.ConsentSource); err != nil {
			return Contact{}, err
		}
	} else if in.ConsentSource != nil {
		if err := validateConsentSource(*in.ConsentSource); err != nil {
			return Contact{}, err
		}
	}
	var attrs []byte
	if in.Attributes != nil {
		var err error
		if attrs, err = ValidateAttributes(in.Attributes); err != nil {
			return Contact{}, err
		}
	}
	row, err := store.New(s.pool).UpdateContact(ctx, store.UpdateContactParams{
		ID: id, ProjectID: projectID, Status: in.Status, ConsentSource: in.ConsentSource, Attributes: attrs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Contact{}, ErrNotFound
	}
	if err != nil {
		return Contact{}, fmt.Errorf("update contact: %w", err)
	}
	return fromRow(row)
}

// Delete erases a contact of the project with its memberships.
func (s *Store) Delete(ctx context.Context, projectID, id uuid.UUID) error {
	n, err := store.New(s.pool).DeleteContact(ctx, store.DeleteContactParams{ID: id, ProjectID: projectID})
	if err != nil {
		return fmt.Errorf("delete contact: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns up to limit contacts of the project, newest first, and
// whether more follow.
func (s *Store) List(ctx context.Context, projectID uuid.UUID, f ListFilter, limit int) ([]Contact, bool, error) {
	limit = min(max(limit, 1), MaxPage)
	before := f.After
	if before == uuid.Nil {
		before = uuid.Max
	}
	var status *string
	if f.Status != "" {
		status = &f.Status
	}
	q := store.New(s.pool)
	var rows []store.Contact
	var err error
	if f.Email != "" {
		rows, err = q.ListContactsByEmail(ctx, store.ListContactsByEmailParams{
			ProjectID: projectID, Email: f.Email, BeforeID: before, Status: status, RowLimit: int32(limit + 1), //nolint:gosec // limit ≤ MaxPage
		})
	} else {
		rows, err = q.ListContacts(ctx, store.ListContactsParams{
			ProjectID: projectID, BeforeID: before, Status: status, RowLimit: int32(limit + 1), //nolint:gosec // limit ≤ MaxPage
		})
	}
	if err != nil {
		return nil, false, fmt.Errorf("list contacts: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]Contact, 0, len(rows))
	for _, r := range rows {
		c, err := fromRow(r)
		if err != nil {
			return nil, false, err
		}
		out = append(out, c)
	}
	return out, more, nil
}

// IsStatus reports whether s is a consent status.
func IsStatus(s string) bool { return validStatus(s) }

func isCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// busy turns a lock timeout into ErrBusy.
func busy(err error) error {
	if isCode(err, "55P03") {
		return ErrBusy
	}
	return err
}
