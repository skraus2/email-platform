package templates

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

// Errors callers branch on.
var (
	// ErrNotFound: no such template or version in the project (also for
	// templates of other projects).
	ErrNotFound      = errors.New("template not found")
	ErrSlugTaken     = errors.New("template slug already used in this project")
	ErrTemplateLimit = errors.New("template limit reached")
	ErrVersionLimit  = errors.New("version limit reached")
	ErrStorageLimit  = errors.New("template storage limit reached")
	// ErrBusy: the write could not run now (all write slots taken, a lock
	// wait or the time limit ran out); retry later.
	ErrBusy = errors.New("template writes busy")
)

// RateLimitError means the project used up its template writes for the
// current minute.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string { return "template write rate limit exceeded" }

// Write limits: at most writeSlots creates/publishes run at once per process
// (each may compile about 1 MiB of template source and holds a connection),
// a write waits at most writeWait for a slot and runs at most writeTimeout.
const (
	defaultWriteSlots   = 2
	defaultWriteWait    = time.Second
	defaultWriteTimeout = 10 * time.Second
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// MaxNameLength bounds a template's display name (characters).
const MaxNameLength = 200

// Template is a named template of a project.
type Template struct {
	ID            uuid.UUID
	ProjectID     uuid.UUID
	Slug          string
	Name          string
	LatestVersion int // 0 while no version exists
	CreatedAt     time.Time
}

// Version is one immutable published version.
type Version struct {
	TemplateID uuid.UUID
	ProjectID  uuid.UUID
	Version    int
	Source     Source
	// Schema is the stored canonical schema (JSON object).
	Schema    []byte
	CreatedAt time.Time
}

// Compile prepares a stored version for rendering.
func (v Version) Compile() (*Compiled, error) {
	schema, err := ParseSchema(v.Schema)
	if err != nil {
		return nil, fmt.Errorf("stored schema of template %s version %d: %w", v.TemplateID, v.Version, err)
	}
	// The publish policy was applied when the version was stored; a later,
	// stricter policy applies to new versions only, never to published ones.
	c, err := compile(v.Source, schema, false)
	if err != nil {
		return nil, fmt.Errorf("stored template %s version %d: %w", v.TemplateID, v.Version, err)
	}
	return c, nil
}

// Store persists templates and versions.
type Store struct {
	pool         *pgxpool.Pool
	maxTemplates int
	maxVersions  int
	maxBytes     int64
	perMinute    int
	writeSlots   chan struct{}
	writeWait    time.Duration
	writeTimeout time.Duration
}

// NewStore caps templates per project, versions per template and the bytes
// of template source a project stores over all versions (versions cannot be
// deleted, so this bounds what one project can put into the database).
// perMinute caps template write attempts (creates and publishes, also
// refused ones) per project and minute.
func NewStore(pool *pgxpool.Pool, maxTemplates, maxVersions int, maxBytes int64, perMinute int) *Store {
	return &Store{
		pool: pool, maxTemplates: max(maxTemplates, 1), maxVersions: max(maxVersions, 1), maxBytes: max(maxBytes, 1),
		perMinute: max(perMinute, 1), writeSlots: make(chan struct{}, defaultWriteSlots),
		writeWait: defaultWriteWait, writeTimeout: defaultWriteTimeout,
	}
}

// beginWrite takes a write slot and counts the attempt against the
// project's per-minute budget. The returned context carries the write time
// limit; done releases the slot.
func (s *Store) beginWrite(ctx context.Context, projectID uuid.UUID) (context.Context, func(), error) {
	wctx, cancel := context.WithTimeout(ctx, s.writeTimeout)
	// Counted before waiting for a slot, so requests turned away as busy
	// use up the project's budget too.
	row, err := store.New(s.pool).CountTemplateWrite(wctx, projectID)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23503":
		cancel()
		return nil, nil, ErrNotFound // no such project
	case err != nil:
		err = s.writeError(ctx, wctx, fmt.Errorf("count template write: %w", err))
		cancel()
		return nil, nil, err
	case int(row.Count) > s.perMinute:
		cancel()
		return nil, nil, &RateLimitError{RetryAfter: time.Duration(min(max(row.RetryAfterSeconds, 1), 60)) * time.Second}
	}
	slots := s.writeSlots // released into the same channel it was taken from
	wait := time.NewTimer(s.writeWait)
	defer wait.Stop()
	select {
	case slots <- struct{}{}:
	case <-wait.C:
		cancel()
		return nil, nil, ErrBusy
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
	return wctx, func() { cancel(); <-slots }, nil
}

// writeError turns our own time limit and lock timeouts into ErrBusy; the
// caller's cancellation stays as it is.
func (s *Store) writeError(ctx, wctx context.Context, err error) error {
	var pgErr *pgconn.PgError
	if ctx.Err() == nil && (wctx.Err() != nil || (errors.As(err, &pgErr) && pgErr.Code == "55P03")) {
		return ErrBusy
	}
	return err
}

func templateFromRow(r store.Template) Template {
	return Template{ID: r.ID, ProjectID: r.ProjectID, Slug: r.Slug, Name: r.Name,
		LatestVersion: int(r.LatestVersion), CreatedAt: r.CreatedAt}
}

func versionFromRow(r store.TemplateVersion) Version {
	v := Version{TemplateID: r.TemplateID, ProjectID: r.ProjectID, Version: int(r.Version),
		Source: Source{Subject: r.Subject, Text: r.TextBody}, Schema: r.Variables, CreatedAt: r.CreatedAt}
	if r.HtmlBody != nil {
		v.Source.HTML = *r.HtmlBody
	}
	return v
}

// Create adds a template without versions. Slug and name errors are *Error
// (validation_failed).
func (s *Store) Create(ctx context.Context, projectID uuid.UUID, slug, name string) (Template, error) {
	if !slugPattern.MatchString(slug) {
		return Template{}, fail(CodeValidation, "slug", "must match ^[a-z0-9][a-z0-9-]{0,62}$")
	}
	if err := validateName(name); err != nil {
		return Template{}, err
	}
	wctx, done, err := s.beginWrite(ctx, projectID)
	if err != nil {
		return Template{}, err
	}
	defer done()
	var t Template
	err = pgx.BeginFunc(wctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetTemplateLockTimeout(wctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		if _, err := q.LockProjectForTemplates(wctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock project: %w", err)
		}
		n, err := q.CountTemplates(wctx, projectID)
		if err != nil {
			return fmt.Errorf("count templates: %w", err)
		}
		if n >= int64(s.maxTemplates) {
			return ErrTemplateLimit
		}
		row, err := q.InsertTemplate(wctx, store.InsertTemplateParams{ProjectID: projectID, Slug: slug, Name: name})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrSlugTaken
		}
		if err != nil {
			return fmt.Errorf("insert template: %w", err)
		}
		t = templateFromRow(row)
		return nil
	})
	if err != nil {
		return Template{}, s.writeError(ctx, wctx, err)
	}
	return t, nil
}

// Publish validates src and the raw schema and stores them as the next
// version of the template. Validation errors are *Error.
func (s *Store) Publish(ctx context.Context, projectID, templateID uuid.UUID, src Source, rawSchema []byte) (Version, error) {
	wctx, done, err := s.beginWrite(ctx, projectID)
	if err != nil {
		return Version{}, err
	}
	defer done()
	// Unknown and foreign templates are refused before the costly compile.
	if _, err := store.New(s.pool).GetTemplate(wctx, store.GetTemplateParams{ID: templateID, ProjectID: projectID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Version{}, ErrNotFound
		}
		return Version{}, s.writeError(ctx, wctx, fmt.Errorf("get template: %w", err))
	}
	schema, err := ParseSchema(rawSchema)
	if err != nil {
		return Version{}, err
	}
	if _, err := Compile(src, schema); err != nil {
		return Version{}, err
	}
	canonical := schema.Canonical()
	size := int64(len(src.Subject) + len(src.Text) + len(src.HTML) + len(canonical))
	var v Version
	err = pgx.BeginFunc(wctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetTemplateLockTimeout(wctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		// The project lock (taken before the template row lock, as in
		// Create) makes the storage check hold under concurrent publishes.
		if _, err := q.LockProjectForTemplates(wctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock project: %w", err)
		}
		used, err := q.TemplateStorageBytes(wctx, projectID)
		if err != nil {
			return fmt.Errorf("template storage: %w", err)
		}
		if used+size > s.maxBytes {
			return ErrStorageLimit
		}
		next, err := q.NextTemplateVersion(wctx, store.NextTemplateVersionParams{
			ID: templateID, ProjectID: projectID, MaxVersions: int32(s.maxVersions), //nolint:gosec // capped by config
		})
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := q.GetTemplate(wctx, store.GetTemplateParams{ID: templateID, ProjectID: projectID}); errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return fmt.Errorf("get template: %w", err)
			}
			return ErrVersionLimit
		}
		if err != nil {
			return fmt.Errorf("next version: %w", err)
		}
		var html *string
		if src.HTML != "" {
			html = &src.HTML
		}
		row, err := q.InsertTemplateVersion(wctx, store.InsertTemplateVersionParams{
			TemplateID: templateID, ProjectID: projectID, Version: next,
			Subject: src.Subject, TextBody: src.Text, HtmlBody: html, Variables: canonical,
		})
		if err != nil {
			return fmt.Errorf("insert version: %w", err)
		}
		v = versionFromRow(row)
		return nil
	})
	if err != nil {
		return Version{}, s.writeError(ctx, wctx, err)
	}
	return v, nil
}

// Get returns a template of the project.
func (s *Store) Get(ctx context.Context, projectID, id uuid.UUID) (Template, error) {
	row, err := store.New(s.pool).GetTemplate(ctx, store.GetTemplateParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	if err != nil {
		return Template{}, fmt.Errorf("get template: %w", err)
	}
	return templateFromRow(row), nil
}

// GetVersion returns a version of a template of the project; version 0
// means the latest one.
func (s *Store) GetVersion(ctx context.Context, projectID, id uuid.UUID, version int) (Version, error) {
	return Resolve(ctx, store.New(s.pool), projectID, id, version)
}

// Resolve loads a version with q (pool or transaction); version 0 means the
// latest. A template without versions is ErrNotFound.
func Resolve(ctx context.Context, q *store.Queries, projectID, id uuid.UUID, version int) (Version, error) {
	if version < 0 || version > 1<<30 {
		return Version{}, ErrNotFound
	}
	if version == 0 {
		t, err := q.GetTemplate(ctx, store.GetTemplateParams{ID: id, ProjectID: projectID})
		if errors.Is(err, pgx.ErrNoRows) {
			return Version{}, ErrNotFound
		}
		if err != nil {
			return Version{}, fmt.Errorf("get template: %w", err)
		}
		if t.LatestVersion == 0 {
			return Version{}, ErrNotFound
		}
		version = int(t.LatestVersion)
	}
	row, err := q.GetTemplateVersion(ctx, store.GetTemplateVersionParams{
		TemplateID: id, ProjectID: projectID, Version: int32(version), // bounded above
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("get template version: %w", err)
	}
	return versionFromRow(row), nil
}

// validateName accepts printable display names: no control or bidi
// formatting characters, valid UTF-8, at most MaxNameLength characters.
func validateName(name string) error {
	if !utf8.ValidString(name) {
		return fail(CodeValidation, "name", "must be valid UTF-8")
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return fail(CodeValidation, "name", "must be at most %d characters", MaxNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Zl, unicode.Zp) {
			return fail(CodeValidation, "name", "must not contain control or text-direction characters")
		}
	}
	return nil
}
