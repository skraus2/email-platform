package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
)

// Automation statuses.
const (
	StatusActive   = "active"
	StatusPaused   = "paused"
	StatusArchived = "archived"
)

// IsStatus reports whether s is an automation status.
func IsStatus(s string) bool { return s == StatusActive || s == StatusPaused || s == StatusArchived }

// Errors callers branch on.
var (
	// ErrNotFound: no such automation in the project (also for automations
	// of other projects).
	ErrNotFound           = errors.New("automation not found")
	ErrTemplateNotFound   = errors.New("template not found")
	ErrMissingUnsubscribe = broadcasts.ErrMissingUnsubscribe
	ErrLimit              = errors.New("automation limit reached")
	// ErrRateLimited: the project created as many automations in the last
	// hour as it may (archived ones count).
	ErrRateLimited = errors.New("automation create rate limit reached")
	ErrArchived    = errors.New("automation is archived")
	// ErrBusy: a lock wait ran out; retry later.
	ErrBusy = errors.New("automations busy")
)

// DefaultMaxPerProject caps the automations of a project that are not
// archived unless NewStore gets another value.
const DefaultMaxPerProject = 50

// DefaultCreatesPerHour caps automation creates per project and hour unless
// LimitCreates sets another value (archived automations stay stored).
const DefaultCreatesPerHour = 100

// MaxPage bounds one list page.
const MaxPage = 100

// Automation is the client-visible state of an automation.
type Automation struct {
	ID           uuid.UUID
	ProjectID    uuid.UUID
	Name         string
	Status       string
	TriggerEvent string
	AllowReentry bool
	Steps        []Step
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// RunCounts is set by Get only: runs per status.
	RunCounts map[string]int
}

func fromRow(r store.Automation) (Automation, error) {
	steps, err := DecodeSteps(r.Steps)
	if err != nil {
		return Automation{}, err
	}
	return Automation{
		ID: r.ID, ProjectID: r.ProjectID, Name: r.Name, Status: r.Status, TriggerEvent: r.TriggerEvent,
		AllowReentry: r.AllowReentry, Steps: steps, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateInput is a new automation. Steps is the raw JSON array of the
// request.
type CreateInput struct {
	Name         string
	TriggerEvent string
	AllowReentry bool
	// Status is StatusActive (default when nil) or StatusPaused.
	Status *string
	Steps  json.RawMessage
}

// UpdateInput changes the name and/or the status (nil: unchanged).
type UpdateInput struct {
	Name   *string
	Status *string
}

// ArchiveEnqueuer inserts the run cancellation job of an archived
// automation inside the caller's transaction.
type ArchiveEnqueuer interface {
	EnqueueAutomationArchive(ctx context.Context, tx pgx.Tx, args jobs.AutomationArchiveArgs) error
}

// Store persists automation definitions.
type Store struct {
	pool          *pgxpool.Pool
	enq           ArchiveEnqueuer
	maxPerProject int
	perHour       int
	requireSender bool
}

// NewStore caps the automations of a project that are not archived at
// maxPerProject.
func NewStore(pool *pgxpool.Pool, enq ArchiveEnqueuer, maxPerProject int) *Store {
	return &Store{pool: pool, enq: enq, maxPerProject: max(maxPerProject, 1), perHour: DefaultCreatesPerHour}
}

// LimitCreates caps how many automations a project may create per hour.
func (s *Store) LimitCreates(perHour int) { s.perHour = max(perHour, 1) }

// RequireVerifiedSenders makes Create refuse a send step whose from address
// has no verified sender identity of the project.
func (s *Store) RequireVerifiedSenders() { s.requireSender = true }

func validateName(name string) error {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxNameLength {
		return invalid("name", "must be valid UTF-8 of 1 to %d characters", MaxNameLength)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Zl, unicode.Zp) {
			return invalid("name", "must not contain control or text-direction characters")
		}
	}
	return nil
}

// Create validates the definition and stores it. Send steps are checked
// like broadcasts: template version of the project ("published" resolved
// now), unsubscribe link, shared variables, sender. Validation errors are
// *Error or *templates.Error.
func (s *Store) Create(ctx context.Context, projectID uuid.UUID, in CreateInput) (Automation, error) {
	if err := validateName(in.Name); err != nil {
		return Automation{}, err
	}
	if !ValidEventName(in.TriggerEvent) {
		return Automation{}, invalid("trigger.event", "must match ^[a-z0-9][a-z0-9_.:-]{0,99}$")
	}
	status := StatusActive
	if in.Status != nil {
		if *in.Status != StatusActive && *in.Status != StatusPaused {
			return Automation{}, invalid("status", "must be active or paused")
		}
		status = *in.Status
	}
	steps, err := ParseSteps(in.Steps)
	if err != nil {
		return Automation{}, err
	}
	q := store.New(s.pool)
	// Cheap read-only pre-check: a project at its cap or create rate does no
	// template work. The checks under the project lock below are
	// authoritative.
	if err := s.refuse(ctx, q, projectID); err != nil {
		return Automation{}, err
	}
	for i := range steps {
		if steps[i].Type != StepSend {
			continue
		}
		if err := s.checkSend(ctx, q, projectID, fmt.Sprintf("steps[%d]", i), &steps[i]); err != nil {
			return Automation{}, err
		}
	}
	stored, err := EncodeSteps(steps)
	if err != nil {
		return Automation{}, err
	}
	if len(stored) > MaxDefinitionSize {
		return Automation{}, invalid("steps", "must be at most %d bytes as stored", MaxDefinitionSize)
	}

	var row store.Automation
	err = lockedTx(ctx, s.pool, func(q *store.Queries) error {
		if _, err := q.LockProjectForAutomations(ctx, projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("lock project: %w", err)
		}
		if err := s.refuse(ctx, q, projectID); err != nil {
			return err
		}
		var err error
		row, err = q.InsertAutomation(ctx, store.InsertAutomationParams{
			ProjectID: projectID, Name: in.Name, Status: status, TriggerEvent: in.TriggerEvent,
			AllowReentry: in.AllowReentry, Steps: stored,
		})
		if err != nil {
			return fmt.Errorf("insert automation: %w", err)
		}
		return nil
	})
	if err != nil {
		return Automation{}, err
	}
	return fromRow(row)
}

// refuse returns ErrLimit or ErrRateLimited when the project may not create
// another automation now.
func (s *Store) refuse(ctx context.Context, q *store.Queries, projectID uuid.UUID) error {
	n, err := q.CountLiveAutomations(ctx, projectID)
	if err != nil {
		return fmt.Errorf("count automations: %w", err)
	}
	if n >= int64(s.maxPerProject) {
		return ErrLimit
	}
	recent, err := q.CountRecentAutomations(ctx, projectID)
	if err != nil {
		return fmt.Errorf("count recent automations: %w", err)
	}
	if recent >= int64(s.perHour) {
		return ErrRateLimited
	}
	return nil
}

// checkSend validates a send step against the database and resolves its
// template version. Errors name the step's fields.
func (s *Store) checkSend(ctx context.Context, q *store.Queries, projectID uuid.UUID, field string, st *Step) error {
	if err := emails.ValidateAddress(st.From); err != nil {
		return invalid(field+".from", "must be a single plain ASCII email address")
	}
	if err := broadcasts.ValidateURLTemplate(st.UnsubscribeURL); err != nil {
		return prefixed(field, err)
	}
	v, err := templates.Resolve(ctx, q, projectID, st.TemplateID, st.TemplateVersion)
	if errors.Is(err, templates.ErrNotFound) {
		return ErrTemplateNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve template: %w", err)
	}
	if err := broadcasts.CheckVersion(v, st.UnsubscribeURL); err != nil {
		return prefixed(field, err)
	}
	schema, err := templates.ParseSchema(v.Schema)
	if err != nil {
		return fmt.Errorf("stored schema: %w", err)
	}
	vars, err := broadcasts.SharedVariables(schema, st.Variables)
	if err != nil {
		return prefixed(field, err)
	}
	if s.requireSender {
		ok, err := emails.SenderVerified(ctx, q, projectID, st.From)
		if err != nil {
			return err
		}
		if !ok {
			return emails.ErrSenderNotVerified
		}
	}
	st.TemplateVersion = v.Version
	st.Variables = vars
	return nil
}

// prefixed puts a step's field path in front of the field of a broadcast or
// template validation error; other errors pass unchanged.
func prefixed(field string, err error) error {
	if be, ok := errors.AsType[*broadcasts.Error](err); ok {
		return &Error{Field: field + "." + be.Field, Reason: be.Reason}
	}
	if te, ok := errors.AsType[*templates.Error](err); ok {
		// Template errors name "variables" or "variables.<name>" already.
		return &templates.Error{Code: te.Code, Field: field + "." + te.Field, Reason: te.Reason}
	}
	return err
}

// Get returns an automation of the project with its run counts.
func (s *Store) Get(ctx context.Context, projectID, id uuid.UUID) (Automation, error) {
	q := store.New(s.pool)
	row, err := q.GetAutomation(ctx, store.GetAutomationParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Automation{}, ErrNotFound
	}
	if err != nil {
		return Automation{}, fmt.Errorf("get automation: %w", err)
	}
	a, err := fromRow(row)
	if err != nil {
		return Automation{}, err
	}
	a.RunCounts, err = runCounts(ctx, q, projectID, id)
	if err != nil {
		return Automation{}, err
	}
	return a, nil
}

// List returns up to limit automations of the project after the cursor,
// newest first.
func (s *Store) List(ctx context.Context, projectID uuid.UUID, status string, after uuid.UUID, limit int) ([]Automation, bool, error) {
	limit = min(max(limit, 1), MaxPage)
	before := after
	if before == uuid.Nil {
		before = uuid.Max
	}
	var st *string
	if status != "" {
		st = &status
	}
	rows, err := store.New(s.pool).ListAutomations(ctx, store.ListAutomationsParams{
		ProjectID: projectID, BeforeID: before, Status: st, RowLimit: int32(limit + 1), // limit ≤ MaxPage
	})
	if err != nil {
		return nil, false, fmt.Errorf("list automations: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]Automation, 0, len(rows))
	for _, r := range rows {
		a, err := fromRow(r)
		if err != nil {
			return nil, false, err
		}
		out = append(out, a)
	}
	return out, more, nil
}

// Update renames an automation and/or changes its status. Archiving is
// final: a job enqueued in the same transaction cancels the automation's
// active runs in batches (until then, step jobs and dispatch treat them as
// canceled).
func (s *Store) Update(ctx context.Context, projectID, id uuid.UUID, in UpdateInput) (Automation, error) {
	if in.Name == nil && in.Status == nil {
		return Automation{}, invalid("body", "must change name or status")
	}
	if in.Name != nil {
		if err := validateName(*in.Name); err != nil {
			return Automation{}, err
		}
	}
	if in.Status != nil && !IsStatus(*in.Status) {
		return Automation{}, invalid("status", "must be active, paused or archived")
	}
	var row store.Automation
	err := lockedTxFunc(ctx, s.pool, func(tx pgx.Tx, q *store.Queries) error {
		cur, err := q.LockAutomation(ctx, store.LockAutomationParams{ID: id, ProjectID: projectID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock automation: %w", err)
		}
		if cur.Status == StatusArchived {
			return ErrArchived
		}
		name, status := cur.Name, cur.Status
		if in.Name != nil {
			name = *in.Name
		}
		if in.Status != nil {
			status = *in.Status
		}
		if row, err = q.UpdateAutomation(ctx, store.UpdateAutomationParams{
			ID: id, ProjectID: projectID, Name: name, Status: status,
		}); err != nil {
			return fmt.Errorf("update automation: %w", err)
		}
		if status == StatusArchived {
			return s.enq.EnqueueAutomationArchive(ctx, tx, jobs.AutomationArchiveArgs{AutomationID: id, ProjectID: projectID})
		}
		return nil
	})
	if err != nil {
		return Automation{}, err
	}
	return fromRow(row)
}

// lockedTx runs fn in a transaction with bounded lock and statement waits.
func lockedTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *store.Queries) error) error {
	return lockedTxFunc(ctx, pool, func(_ pgx.Tx, q *store.Queries) error { return fn(q) })
}

// lockedTxFunc is lockedTx for functions that also need the transaction.
func lockedTxFunc(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx, q *store.Queries) error) error {
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.SetContactLockTimeout(ctx); err != nil {
			return fmt.Errorf("lock timeout: %w", err)
		}
		if err := q.SetContactStatementTimeout(ctx); err != nil {
			return fmt.Errorf("statement timeout: %w", err)
		}
		return fn(tx, q)
	})
	return busy(err)
}

// busy maps lock and statement timeouts to ErrBusy.
func busy(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && slices.Contains([]string{"55P03", "57014"}, pe.Code) {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return err
}

// RunStatuses are the statuses runs are counted by.
var RunStatuses = []string{RunActive, RunCompleted, RunExited, RunCanceled, RunFailed}

// Run statuses.
const (
	RunActive    = "active"
	RunCompleted = "completed"
	RunExited    = "exited"
	RunCanceled  = "canceled"
	RunFailed    = "failed"
)

func runCounts(ctx context.Context, q *store.Queries, projectID, id uuid.UUID) (map[string]int, error) {
	rows, err := q.CountAutomationRuns(ctx, store.CountAutomationRunsParams{AutomationID: id, ProjectID: projectID})
	if err != nil {
		return nil, fmt.Errorf("count runs: %w", err)
	}
	counts := make(map[string]int, len(RunStatuses))
	for _, s := range RunStatuses {
		counts[s] = 0
	}
	for _, r := range rows {
		counts[r.Status] = int(r.N)
	}
	return counts, nil
}

// ArchiveBatch is how many runs one archive job run cancels.
const ArchiveBatch = 1000

// archiveRetry is the wait before cancelling runs that were locked.
const archiveRetry = 5 * time.Second

// CancelArchivedRuns is the automation_archive job handler: it cancels one
// batch of active runs of an archived automation and snoozes for the next.
func CancelArchivedRuns(pool *pgxpool.Pool) func(ctx context.Context, args jobs.AutomationArchiveArgs, at jobs.Attempt) error {
	return func(ctx context.Context, args jobs.AutomationArchiveArgs, _ jobs.Attempt) error {
		n, err := store.New(pool).CancelArchivedRuns(ctx, store.CancelArchivedRunsParams{
			AutomationID: args.AutomationID, ProjectID: args.ProjectID, RowLimit: ArchiveBatch,
		})
		if err != nil {
			return fmt.Errorf("cancel archived runs: %w", err)
		}
		if n == ArchiveBatch {
			return jobs.Snooze(0) // a full batch: more may be left
		}
		// Runs skipped because another transaction held them (a step job
		// that read the automation before the archive) are tried again; a
		// lock lasts at most one job timeout.
		left, err := store.New(pool).ActiveRunsLeft(ctx, store.ActiveRunsLeftParams{AutomationID: args.AutomationID, ProjectID: args.ProjectID})
		if err != nil {
			return fmt.Errorf("check archived runs: %w", err)
		}
		if left {
			return jobs.Snooze(archiveRetry)
		}
		return nil
	}
}
