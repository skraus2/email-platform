package contacts

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

// Audience errors callers branch on (unknown audiences are ErrNotFound).
var (
	ErrAudienceExists = errors.New("audience name already used in this project")
	ErrAudienceLimit  = errors.New("audience limit reached")
	ErrAudienceFull   = errors.New("audience is full")
)

// Audience limits.
const (
	MaxAudienceNameLength = 200
	MaxAddBatch           = 1000
)

// Audience is a named list of contacts of a project.
type Audience struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Name      string
	CreatedAt time.Time
}

// AddResult counts the outcome of a bulk add.
type AddResult struct {
	Added          int
	AlreadyMembers int
	NotFound       int
}

// Audiences persists audiences and their memberships.
type Audiences struct {
	pool          *pgxpool.Pool
	maxPerProject int
	maxContacts   int
}

// NewAudiences caps the audiences of a project and the contacts of one
// audience.
func NewAudiences(pool *pgxpool.Pool, maxPerProject, maxContacts int) *Audiences {
	return &Audiences{pool: pool, maxPerProject: max(maxPerProject, 1), maxContacts: max(maxContacts, 1)}
}

func audienceFromRow(r store.Audience) Audience {
	return Audience{ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, CreatedAt: r.CreatedAt}
}

func validateAudienceName(name string) error {
	switch {
	case name == "":
		return invalid("name", "must not be empty")
	case !utf8.ValidString(name):
		return invalid("name", "must be valid UTF-8")
	case utf8.RuneCountInString(name) > MaxAudienceNameLength:
		return invalid("name", "must be at most %d characters", MaxAudienceNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Zl, unicode.Zp) {
			return invalid("name", "must not contain control or text-direction characters")
		}
	}
	return nil
}

// lockedTx runs fn in a transaction with bounded lock and statement waits.
func lockedTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *store.Queries) error) error {
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetContactLockTimeout(ctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		if err := q.SetContactStatementTimeout(ctx); err != nil {
			return fmt.Errorf("statement timeout: %w", err)
		}
		return fn(q)
	})
	return busy(err)
}

// Create adds an audience. Name errors are *Error.
func (s *Audiences) Create(ctx context.Context, projectID uuid.UUID, name string) (Audience, error) {
	if err := validateAudienceName(name); err != nil {
		return Audience{}, err
	}
	var row store.Audience
	err := lockedTx(ctx, s.pool, func(q *store.Queries) error {
		if _, err := q.LockProjectForContacts(ctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock project: %w", err)
		}
		n, err := q.CountAudiences(ctx, projectID)
		if err != nil {
			return fmt.Errorf("count audiences: %w", err)
		}
		if n >= int64(s.maxPerProject) {
			return ErrAudienceLimit
		}
		row, err = q.InsertAudience(ctx, store.InsertAudienceParams{ProjectID: projectID, Name: name})
		if isCode(err, "23505") {
			return ErrAudienceExists
		}
		if err != nil {
			return fmt.Errorf("insert audience: %w", err)
		}
		return nil
	})
	if err != nil {
		return Audience{}, err
	}
	return audienceFromRow(row), nil
}

// Get returns an audience of the project and its member count.
func (s *Audiences) Get(ctx context.Context, projectID, id uuid.UUID) (Audience, int64, error) {
	q := store.New(s.pool)
	row, err := q.GetAudience(ctx, store.GetAudienceParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Audience{}, 0, ErrNotFound
	}
	if err != nil {
		return Audience{}, 0, fmt.Errorf("get audience: %w", err)
	}
	n, err := q.CountAudienceMembers(ctx, store.CountAudienceMembersParams{AudienceID: id, ProjectID: projectID})
	if err != nil {
		return Audience{}, 0, fmt.Errorf("count members: %w", err)
	}
	return audienceFromRow(row), n, nil
}

// List returns up to limit audiences of the project, newest first, after
// the cursor (uuid.Nil for the first page), and whether more follow.
func (s *Audiences) List(ctx context.Context, projectID, after uuid.UUID, limit int) ([]Audience, bool, error) {
	limit = min(max(limit, 1), MaxPage)
	before := after
	if before == uuid.Nil {
		before = uuid.Max
	}
	rows, err := store.New(s.pool).ListAudiences(ctx, store.ListAudiencesParams{
		ProjectID: projectID, BeforeID: before, RowLimit: int32(limit + 1), //nolint:gosec // limit ≤ MaxPage
	})
	if err != nil {
		return nil, false, fmt.Errorf("list audiences: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]Audience, 0, len(rows))
	for _, r := range rows {
		out = append(out, audienceFromRow(r))
	}
	return out, more, nil
}

// Add adds the contacts of the project among ids (1–MaxAddBatch distinct
// ids) to the audience. An add that would take the audience over its cap
// adds nothing (ErrAudienceFull).
func (s *Audiences) Add(ctx context.Context, projectID, audienceID uuid.UUID, ids []uuid.UUID) (AddResult, error) {
	if len(ids) == 0 || len(ids) > MaxAddBatch {
		return AddResult{}, invalid("contact_ids", "must hold 1 to %d ids", MaxAddBatch)
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return AddResult{}, invalid("contact_ids", "must not repeat an id")
		}
		seen[id] = true
	}
	var res AddResult
	err := lockedTx(ctx, s.pool, func(q *store.Queries) error {
		if _, err := q.LockAudience(ctx, store.LockAudienceParams{ID: audienceID, ProjectID: projectID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock audience: %w", err)
		}
		found, err := q.CountProjectContactsIn(ctx, store.CountProjectContactsInParams{ProjectID: projectID, Ids: ids})
		if err != nil {
			return fmt.Errorf("count contacts: %w", err)
		}
		already, err := q.CountAudienceMembersIn(ctx, store.CountAudienceMembersInParams{AudienceID: audienceID, ProjectID: projectID, Ids: ids})
		if err != nil {
			return fmt.Errorf("count members: %w", err)
		}
		members, err := q.CountAudienceMembers(ctx, store.CountAudienceMembersParams{AudienceID: audienceID, ProjectID: projectID})
		if err != nil {
			return fmt.Errorf("count members: %w", err)
		}
		if members+found-already > int64(s.maxContacts) {
			return ErrAudienceFull
		}
		added, err := q.AddAudienceMembers(ctx, store.AddAudienceMembersParams{AudienceID: audienceID, ProjectID: projectID, Ids: ids})
		if err != nil {
			return fmt.Errorf("add members: %w", err)
		}
		res = AddResult{Added: int(added), AlreadyMembers: int(already), NotFound: len(ids) - int(found)}
		return nil
	})
	if isCode(err, "23503") {
		// A contact was deleted while it was being added.
		return AddResult{}, ErrBusy
	}
	return res, err
}

// AddOne adds one contact of the project to the audience (idempotent).
func (s *Audiences) AddOne(ctx context.Context, projectID, audienceID, contactID uuid.UUID) error {
	res, err := s.Add(ctx, projectID, audienceID, []uuid.UUID{contactID})
	if err == nil && res.NotFound > 0 {
		return ErrNotFound
	}
	return err
}

// Remove takes a contact out of the audience; both must exist in the
// project, membership need not.
func (s *Audiences) Remove(ctx context.Context, projectID, audienceID, contactID uuid.UUID) error {
	q := store.New(s.pool)
	if _, err := q.GetAudience(ctx, store.GetAudienceParams{ID: audienceID, ProjectID: projectID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("get audience: %w", err)
	}
	if _, err := q.GetContact(ctx, store.GetContactParams{ID: contactID, ProjectID: projectID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("get contact: %w", err)
	}
	if err := q.RemoveAudienceMember(ctx, store.RemoveAudienceMemberParams{AudienceID: audienceID, ContactID: contactID, ProjectID: projectID}); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

// Members returns up to limit member contacts of the audience ordered by
// contact id, after the cursor (uuid.Nil for the first page), and whether
// more follow.
func (s *Audiences) Members(ctx context.Context, projectID, audienceID, after uuid.UUID, limit int) ([]Contact, bool, error) {
	limit = min(max(limit, 1), MaxPage)
	q := store.New(s.pool)
	if _, err := q.GetAudience(ctx, store.GetAudienceParams{ID: audienceID, ProjectID: projectID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, fmt.Errorf("get audience: %w", err)
	}
	rows, err := q.ListAudienceContacts(ctx, store.ListAudienceContactsParams{
		AudienceID: audienceID, ProjectID: projectID, AfterID: after, RowLimit: int32(limit + 1), //nolint:gosec // limit ≤ MaxPage
	})
	if err != nil {
		return nil, false, fmt.Errorf("list members: %w", err)
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
