package automations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/store"
)

// Errors of run operations callers branch on.
var (
	// ErrRunNotFound: no such run in the project (also for runs of other
	// projects).
	ErrRunNotFound = errors.New("automation run not found")
	// ErrRunNotCancelable: the run already ended.
	ErrRunNotCancelable = errors.New("automation run is not active")
)

// IsRunStatus reports whether s is a run status.
func IsRunStatus(s string) bool {
	for _, r := range RunStatuses {
		if s == r {
			return true
		}
	}
	return false
}

// Run is the client-visible state of a run (never its trigger data).
type Run struct {
	ID           uuid.UUID
	ProjectID    uuid.UUID
	AutomationID uuid.UUID
	ContactID    uuid.UUID
	Status       string
	Reason       *string
	Step         int
	NextStepAt   *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	FinishedAt   *time.Time
	// Emails is set by GetRun only.
	Emails []RunEmail
}

// RunEmail is an email a run created.
type RunEmail struct {
	ID     uuid.UUID
	Step   int
	Status string
}

func runFromRow(r store.GetRunRow) Run {
	return Run{
		ID: r.ID, ProjectID: r.ProjectID, AutomationID: r.AutomationID, ContactID: r.ContactID, Status: r.Status,
		Reason: r.Reason, Step: int(r.Step), NextStepAt: r.NextStepAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		FinishedAt: r.FinishedAt,
	}
}

// Runs reads and cancels runs.
type Runs struct {
	pool *pgxpool.Pool
}

// NewRuns builds the run reader.
func NewRuns(pool *pgxpool.Pool) *Runs { return &Runs{pool: pool} }

// Get returns a run of the project with the emails it created.
func (s *Runs) Get(ctx context.Context, projectID, id uuid.UUID) (Run, error) {
	q := store.New(s.pool)
	row, err := q.GetRun(ctx, store.GetRunParams{ID: id, ProjectID: projectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get run: %w", err)
	}
	run := runFromRow(row)
	emails, err := q.EmailsOfRun(ctx, store.EmailsOfRunParams{AutomationRunID: &id, ProjectID: projectID})
	if err != nil {
		return Run{}, fmt.Errorf("emails of run: %w", err)
	}
	run.Emails = make([]RunEmail, 0, len(emails))
	for _, e := range emails {
		step := 0
		if e.AutomationStep != nil {
			step = int(*e.AutomationStep)
		}
		run.Emails = append(run.Emails, RunEmail{ID: e.ID, Step: step, Status: e.Status})
	}
	return run, nil
}

// RunFilter narrows a run list. Zero values mean "any".
type RunFilter struct {
	Status    string
	ContactID uuid.UUID
}

// List returns up to limit runs of an automation of the project after the
// cursor, newest first. An automation of another project is ErrNotFound.
func (s *Runs) List(ctx context.Context, projectID, automationID uuid.UUID, f RunFilter, after uuid.UUID, limit int) ([]Run, bool, error) {
	q := store.New(s.pool)
	if _, err := q.GetAutomation(ctx, store.GetAutomationParams{ID: automationID, ProjectID: projectID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, fmt.Errorf("get automation: %w", err)
	}
	limit = min(max(limit, 1), MaxPage)
	before := after
	if before == uuid.Nil {
		before = uuid.Max
	}
	params := store.ListRunsParams{ProjectID: projectID, AutomationID: automationID, BeforeID: before, RowLimit: int32(limit + 1)} //nolint:gosec // limit ≤ MaxPage
	if f.Status != "" {
		params.Status = &f.Status
	}
	if f.ContactID != uuid.Nil {
		params.ContactID = &f.ContactID
	}
	rows, err := q.ListRuns(ctx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list runs: %w", err)
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, runFromRow(store.GetRunRow(r)))
	}
	return out, more, nil
}

// Cancel ends an active run of the project (reason canceled). Its queued
// emails are canceled by dispatch.
func (s *Runs) Cancel(ctx context.Context, projectID, id uuid.UUID) (Run, error) {
	var run Run
	err := lockedTx(ctx, s.pool, func(q *store.Queries) error {
		row, err := q.CancelRun(ctx, store.CancelRunParams{ID: id, ProjectID: projectID})
		if err == nil {
			run = runFromRow(store.GetRunRow(row))
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("cancel run: %w", err)
		}
		if _, err := q.GetRun(ctx, store.GetRunParams{ID: id, ProjectID: projectID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRunNotFound
			}
			return fmt.Errorf("get run: %w", err)
		}
		return ErrRunNotCancelable
	})
	return run, err
}
