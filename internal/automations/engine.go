package automations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/logging"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
)

// Run end reasons.
const (
	ReasonConditionNotMet    = "condition_not_met"
	ReasonUnsubscribed       = "unsubscribed"
	ReasonSuppressed         = "suppressed"
	ReasonProjectDisabled    = "project_disabled"
	ReasonCanceled           = "canceled"
	ReasonAutomationArchived = "automation_archived"
	ReasonRenderFailed       = "render_failed"
	ReasonInternalError      = "internal_error"
)

// EngineEnqueuer inserts step and send jobs inside the caller's
// transaction.
type EngineEnqueuer interface {
	StepEnqueuer
	EnqueueSendEmail(ctx context.Context, tx pgx.Tx, args jobs.SendEmailArgs, opts jobs.SendOpts) error
}

// Engine executes the due steps of runs (the automation_step job handler).
type Engine struct {
	pool     *pgxpool.Pool
	enq      EngineEnqueuer
	logger   *slog.Logger
	compiled *templates.Cache
	now      func() time.Time
}

// NewEngine builds the step handler; compiled caches template versions.
func NewEngine(pool *pgxpool.Pool, enq EngineEnqueuer, logger *slog.Logger, compiled *templates.Cache) *Engine {
	return &Engine{pool: pool, enq: enq, logger: logger, compiled: compiled, now: time.Now}
}

// outcome is how a step job run left its run.
type outcome struct {
	status, reason string
	step           int
	waitUntil      time.Time
	emails         []uuid.UUID // created in this job run
	recipient      string
}

// Step is the automation_step job handler. In one transaction it locks the
// run's contact (shared) and then the run, and executes steps from the job's
// step until a wait or the end. A run that is gone, no longer active or at
// another step makes the job a no-op, so repeated and stale jobs do nothing.
func (e *Engine) Step(ctx context.Context, args jobs.AutomationStepArgs, at jobs.Attempt) error {
	log := e.logger.With(
		slog.String("project_id", args.ProjectID.String()),
		slog.String("run_id", args.RunID.String()),
		slog.Int("step", args.Step),
		slog.Int64("job_id", at.JobID),
	)
	var out outcome
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		out, err = e.execute(ctx, tx, args)
		return err
	})
	if err != nil {
		if at.Final() && !errors.Is(ctx.Err(), context.Canceled) {
			// If even the mark fails, the job is discarded with the error
			// and the overdue net picks the run up again.
			return e.failRun(ctx, log, args, err)
		}
		return err
	}
	// Logged only now: a rolled-back job run created nothing.
	for _, id := range out.emails {
		log.InfoContext(ctx, "automation email queued", slog.String("email_id", id.String()), slog.String("recipient", logging.MaskEmail(out.recipient)))
	}
	switch out.status {
	case "":
		// No-op: duplicate, stale or ended run.
	case RunActive:
		log.DebugContext(ctx, "automation run waiting", slog.Int("next_step", out.step), slog.Time("until", out.waitUntil), slog.Int("emails", len(out.emails)))
	default:
		log.InfoContext(ctx, "automation run ended", slog.String("status", out.status), slog.String("reason", out.reason), slog.Int("emails", len(out.emails)))
	}
	return nil
}

func (e *Engine) execute(ctx context.Context, tx pgx.Tx, args jobs.AutomationStepArgs) (outcome, error) {
	q := store.New(tx)
	c, err := q.LockRunContact(ctx, store.LockRunContactParams{RunID: args.RunID, ProjectID: args.ProjectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return outcome{}, nil // the run (and its contact) is gone
	}
	if err != nil {
		return outcome{}, fmt.Errorf("lock contact: %w", err)
	}
	r, err := q.LockRunForStep(ctx, store.LockRunForStepParams{ID: args.RunID, ProjectID: args.ProjectID})
	if errors.Is(err, pgx.ErrNoRows) {
		return outcome{}, nil
	}
	if err != nil {
		return outcome{}, fmt.Errorf("lock run: %w", err)
	}
	if r.Status != RunActive || int(r.Step) != args.Step {
		return outcome{}, nil
	}
	end := func(status, reason string, step int, emails []uuid.UUID) (outcome, error) {
		if err := q.EndRun(ctx, store.EndRunParams{
			ID: r.ID, ProjectID: r.ProjectID, Status: status, Reason: nonEmpty(reason), Step: int16(step), //nolint:gosec // ≤ MaxSteps
		}); err != nil {
			return outcome{}, fmt.Errorf("end run: %w", err)
		}
		return outcome{status: status, reason: reason, step: step, emails: emails, recipient: c.Email}, nil
	}
	switch {
	case r.AutomationStatus == StatusArchived:
		// A trigger can race the archive; its run ends here.
		return end(RunCanceled, ReasonAutomationArchived, args.Step, nil)
	case r.ProjectStatus != "active":
		// Re-enabling the project must not silently resume the run.
		return end(RunExited, ReasonProjectDisabled, args.Step, nil)
	}
	steps, err := DecodeSteps(r.Steps)
	if err != nil {
		return outcome{}, err
	}
	var emails []uuid.UUID
	for i := args.Step; i < len(steps); i++ {
		s := steps[i]
		switch s.Type {
		case StepWait:
			until := e.now().Add(time.Duration(s.Seconds) * time.Second)
			if err := q.WaitRun(ctx, store.WaitRunParams{ID: r.ID, ProjectID: r.ProjectID, Step: int16(i + 1), NextStepAt: &until}); err != nil { //nolint:gosec // ≤ MaxSteps
				return outcome{}, fmt.Errorf("wait: %w", err)
			}
			if err := e.enq.EnqueueAutomationStep(ctx, tx, jobs.AutomationStepArgs{RunID: r.ID, ProjectID: r.ProjectID, Step: i + 1}, until); err != nil {
				return outcome{}, err
			}
			return outcome{status: RunActive, step: i + 1, waitUntil: until, emails: emails, recipient: c.Email}, nil
		case StepCondition:
			ok, err := e.condition(ctx, q, s, c, r.ProjectID, r.CreatedAt)
			if err != nil {
				return outcome{}, err
			}
			if !ok {
				return end(RunExited, ReasonConditionNotMet, i, emails)
			}
		case StepSend:
			switch {
			case c.Status != contacts.StatusSubscribed:
				return end(RunExited, ReasonUnsubscribed, i, emails)
			case r.Suppressed:
				return end(RunExited, ReasonSuppressed, i, emails)
			}
			id, err := e.send(ctx, tx, q, r, c, s, i)
			if _, ok := errors.AsType[*templates.Error](err); ok {
				// The contact's attributes or the event data do not fit
				// the version (never logged: personal data).
				return end(RunFailed, ReasonRenderFailed, i, emails)
			}
			if err != nil {
				return outcome{}, err
			}
			emails = append(emails, id)
		default:
			return outcome{}, fmt.Errorf("unknown step type %q", s.Type)
		}
	}
	return end(RunCompleted, "", len(steps), emails)
}

func (e *Engine) condition(ctx context.Context, q *store.Queries, s Step, c store.LockRunContactRow, projectID uuid.UUID, runStart time.Time) (bool, error) {
	if s.Attribute != "" {
		return matchAttribute(s, c.Attributes)
	}
	occurred, err := q.EventOccurredSince(ctx, store.EventOccurredSinceParams{ContactID: c.ID, ProjectID: projectID, Name: s.Event, Since: runStart})
	if err != nil {
		return false, fmt.Errorf("event condition: %w", err)
	}
	return occurred == (s.Op == OpOccurred), nil
}

// send renders the step's version for the contact and inserts the email,
// its content and its dispatch job.
func (e *Engine) send(ctx context.Context, tx pgx.Tx, q *store.Queries, r store.LockRunForStepRow, c store.LockRunContactRow, s Step, i int) (uuid.UUID, error) {
	compiled := e.compiled.Lookup(r.ProjectID, s.TemplateID, s.TemplateVersion)
	if compiled == nil {
		v, err := templates.Resolve(ctx, q, r.ProjectID, s.TemplateID, s.TemplateVersion)
		if err != nil {
			return uuid.Nil, fmt.Errorf("load template version: %w", err)
		}
		if compiled, _, err = e.compiled.Compiled(v); err != nil {
			return uuid.Nil, fmt.Errorf("compile template version: %v", err) //nolint:errorlint // a stored version is server state, never a client error
		}
	}
	url := contacts.UnsubscribeURL(s.UnsubscribeURL, c.UnsubscribeToken)
	vars, err := templates.MergeLayers(compiled.Schema(), [][]byte{s.Variables, c.Attributes, r.TriggerData},
		map[string]string{contacts.ReservedAttribute: url})
	if err != nil {
		return uuid.Nil, fmt.Errorf("merge variables: %w", err)
	}
	out, err := compiled.Render(vars)
	if err != nil {
		return uuid.Nil, err
	}
	// Subjects are stored and listed; create refuses versions whose subject
	// uses the unsubscribe URL, and this keeps the token out regardless.
	if strings.Contains(out.Subject, c.UnsubscribeToken) {
		return uuid.Nil, &templates.Error{Code: templates.CodeRenderFailed, Field: "subject", Reason: "must not contain the unsubscribe link"}
	}
	version := int32(s.TemplateVersion) //nolint:gosec // versions are capped far below 2^31
	step := int16(i)                    //nolint:gosec // ≤ MaxSteps
	id, err := q.InsertAutomationEmail(ctx, store.InsertAutomationEmailParams{
		ProjectID: r.ProjectID, Sender: s.From, Recipient: c.Email, Subject: out.Subject,
		TemplateID: &s.TemplateID, TemplateVersion: &version, ContactID: &c.ID,
		AutomationID: &r.AutomationID, AutomationRunID: &r.ID, AutomationStep: &step,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert email: %w", err)
	}
	if err := q.InsertEmailContentWithUnsubscribe(ctx, store.InsertEmailContentWithUnsubscribeParams{
		EmailID: id, ProjectID: r.ProjectID, TextBody: nonEmpty(out.Text), HtmlBody: nonEmpty(out.HTML), ListUnsubscribeUrl: &url,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("insert email content: %w", err)
	}
	if err := e.enq.EnqueueSendEmail(ctx, tx, jobs.SendEmailArgs{EmailID: id, ProjectID: r.ProjectID}, jobs.SendOpts{Bulk: true}); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// failRun marks a run failed after its step job's final attempt, unless the
// run moved on meanwhile (a stale job's failure never touches a healthy run).
// It returns an error only when the mark itself failed.
func (e *Engine) failRun(ctx context.Context, log *slog.Logger, args jobs.AutomationStepArgs, cause error) error {
	log.ErrorContext(ctx, "automation step failed on its final attempt", slog.Any("error", cause))
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	n, err := store.New(e.pool).FailRunAtStep(dctx, store.FailRunAtStepParams{
		ID: args.RunID, ProjectID: args.ProjectID, Step: int16(args.Step), //nolint:gosec // ≤ MaxSteps
	})
	if err != nil {
		log.ErrorContext(ctx, "mark automation run failed", slog.Any("error", err))
		return fmt.Errorf("mark run failed: %w", err)
	}
	if n > 0 {
		log.WarnContext(ctx, "automation run failed", slog.String("reason", ReasonInternalError))
	}
	return nil
}

// OverdueGrace is how long past its due time an active run may wait before
// the net re-enqueues its step job.
const OverdueGrace = 15 * time.Minute

// overdueBatch bounds the runs one net pass looks at.
const overdueBatch = 1000

// Requeue is the overdue-run net (run from the periodic cleanup): it
// re-enqueues the step job of active runs overdue by more than OverdueGrace.
// Step jobs are unique by args, so this is a no-op while a job of that step
// still exists; it revives runs whose job was lost.
func (e *Engine) Requeue(ctx context.Context) error {
	var n int
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := store.New(tx).OverdueRuns(ctx, store.OverdueRunsParams{Before: e.now().Add(-OverdueGrace), RowLimit: overdueBatch})
		if err != nil {
			return fmt.Errorf("overdue runs: %w", err)
		}
		for _, r := range rows {
			if err := e.enq.EnqueueAutomationStep(ctx, tx, jobs.AutomationStepArgs{RunID: r.ID, ProjectID: r.ProjectID, Step: int(r.Step)}, time.Time{}); err != nil {
				return err
			}
		}
		n = len(rows)
		return nil
	})
	if err != nil {
		return fmt.Errorf("requeue overdue runs: %w", err)
	}
	if n > 0 {
		e.logger.InfoContext(ctx, "overdue automation runs checked", slog.Int("runs", n))
	}
	return nil
}

// nonEmpty returns nil for "" (no reason, no body part).
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
