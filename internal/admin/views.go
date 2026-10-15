package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
)

// ErrNotFound: no such email, template or version.
var ErrNotFound = errors.New("not found")

// ErrViewBusy: a search ran past its time limit or no preview slot was
// free; retry later.
var ErrViewBusy = errors.New("operator view busy")

// MaxPage bounds one page of every operator list.
const MaxPage = 100

// View limits. Searches share the database pool with the project API, so
// at most searchSlots run at once per process and the server cancels each
// after searchTimeout; at most previewSlots template previews compile at
// once (a compile of the largest template briefly holds about 60 MiB).
const (
	searchTimeout = 5 * time.Second
	searchSlots   = 2
	slotWait      = time.Second
	previewSlots  = 2
)

// Views is the read model of the operator console. It reads every project;
// every row carries its project_id. It never returns email bodies, webhook
// secrets or key hashes.
type Views struct {
	pool     *pgxpool.Pool
	searches chan struct{}
	previews chan struct{}
}

// NewViews builds the read model.
func NewViews(pool *pgxpool.Pool) *Views {
	return &Views{pool: pool, searches: make(chan struct{}, searchSlots), previews: make(chan struct{}, previewSlots)}
}

// slot takes one of slots, waiting at most slotWait; release frees it.
func slot(ctx context.Context, slots chan struct{}) (release func(), err error) {
	wait := time.NewTimer(slotWait)
	defer wait.Stop()
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-wait.C:
		return nil, ErrViewBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// bounded runs fn with a search slot under a server-side statement timeout.
func (v *Views) bounded(ctx context.Context, fn func(q *store.Queries) error) error {
	release, err := slot(ctx, v.searches)
	if err != nil {
		return err
	}
	defer release()
	sctx, cancel := context.WithTimeout(ctx, searchTimeout+time.Second)
	defer cancel()
	err = store.ReadOnlyWithTimeout(sctx, v.pool, searchTimeout, fn)
	if err != nil && ctx.Err() == nil && (sctx.Err() != nil || store.StatementTimedOut(err)) {
		return ErrViewBusy
	}
	return err
}

// q runs queries as unnamed statements: the lists have optional filters,
// and a cached generic plan could scan far more than needed.
func (v *Views) q() *store.Queries { return store.New(store.Unprepared(v.pool)) }

func before(cursor uuid.UUID) uuid.UUID {
	if cursor == uuid.Nil {
		return uuid.Max
	}
	return cursor
}

func checkLimit(limit int) error {
	if limit < 1 || limit > MaxPage {
		return fmt.Errorf("page size must be between 1 and %d", MaxPage)
	}
	return nil
}

// page splits limit+1 rows into a page and whether more follow.
func page[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

// Projects lists projects, newest first, before the cursor id.
func (v *Views) Projects(ctx context.Context, cursor uuid.UUID, limit int) ([]store.Project, bool, error) {
	if err := checkLimit(limit); err != nil {
		return nil, false, err
	}
	rows, err := v.q().AdminListProjects(ctx, store.AdminListProjectsParams{BeforeID: before(cursor), RowLimit: int32(limit + 1)}) //nolint:gosec // bounded
	if err != nil {
		return nil, false, fmt.Errorf("list projects: %w", err)
	}
	p, more := page(rows, limit)
	return p, more, nil
}

// Email is an email as operators see it (never the body).
type Email struct {
	emails.Email
	ProjectName string
	Provider    *string
	ProviderID  *string
}

func emailFromRow(e store.Email, projectName string) Email {
	out := Email{
		Email: emails.Email{
			ID: e.ID, ProjectID: e.ProjectID, Status: e.Status, From: e.Sender, To: e.Recipient, Subject: e.Subject,
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, FinishedAt: e.FinishedAt, Attempts: int(e.Attempts),
			ErrorCode: e.ErrorCode, TemplateID: e.TemplateID,
		},
		ProjectName: projectName, Provider: e.Provider, ProviderID: e.ProviderID,
	}
	if e.TemplateVersion != nil {
		n := int(*e.TemplateVersion)
		out.TemplateVersion = &n
	}
	return out
}

// EmailFilter narrows the operator email search. Query matches an email
// id, or else a provider message id or an exact recipient address
// (case-insensitive).
type EmailFilter struct {
	ProjectID     *uuid.UUID
	Statuses      []string
	ErrorCode     string
	Recipient     string
	TemplateID    *uuid.UUID
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Query         string
	Cursor        uuid.UUID
}

// SearchEmails returns up to limit emails of all projects, newest first.
func (v *Views) SearchEmails(ctx context.Context, f EmailFilter, limit int) ([]Email, bool, error) {
	if err := checkLimit(limit); err != nil {
		return nil, false, err
	}
	p := store.AdminSearchEmailsParams{
		BeforeID: before(f.Cursor), ProjectID: f.ProjectID, Statuses: f.Statuses, TemplateID: f.TemplateID,
		CreatedAfter: f.CreatedAfter, CreatedBefore: f.CreatedBefore, RowLimit: int32(limit + 1), //nolint:gosec // bounded
	}
	if p.Statuses == nil {
		p.Statuses = []string{}
	}
	if f.ErrorCode != "" {
		p.ErrorCode = &f.ErrorCode
	}
	if f.Recipient != "" {
		p.Recipient = &f.Recipient
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		if id, err := uuid.Parse(q); err == nil && len(q) == 36 {
			p.EmailID = &id
		} else {
			// A provider message id or a recipient address (some provider
			// ids look like addresses). Message-IDs are stored with angle
			// brackets but often copied without.
			if !strings.HasPrefix(q, "<") && strings.Contains(q, "@") {
				bracketed := "<" + q + ">"
				p.TextQueryAlt = &bracketed
			}
			p.TextQuery = &q
		}
	}
	var rows []store.AdminSearchEmailsRow
	err := v.bounded(ctx, func(q *store.Queries) (err error) {
		rows, err = q.AdminSearchEmails(ctx, p)
		return err
	})
	if errors.Is(err, ErrViewBusy) {
		return nil, false, err
	}
	if err != nil {
		return nil, false, fmt.Errorf("search emails: %w", err)
	}
	out := make([]Email, 0, min(len(rows), limit))
	for _, r := range rows[:min(len(rows), limit)] {
		out = append(out, emailFromRow(r.Email, r.ProjectName))
	}
	return out, len(rows) > limit, nil
}

// Email returns one email of any project.
func (v *Views) Email(ctx context.Context, id uuid.UUID) (Email, error) {
	r, err := v.q().AdminGetEmail(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Email{}, ErrNotFound
	}
	if err != nil {
		return Email{}, fmt.Errorf("get email: %w", err)
	}
	return emailFromRow(r.Email, r.ProjectName), nil
}

// EmailEvents lists the events of an email in receive order after the
// cursor event.
func (v *Views) EmailEvents(ctx context.Context, id, cursor uuid.UUID, limit int) ([]events.Event, bool, error) {
	if err := checkLimit(limit); err != nil {
		return nil, false, err
	}
	e, err := v.Email(ctx, id)
	if err != nil {
		return nil, false, err
	}
	rows, err := events.List(ctx, store.New(v.pool), e.ProjectID, id, cursor, limit+1)
	if err != nil {
		return nil, false, err
	}
	p, more := page(rows, limit)
	return p, more, nil
}

// EmailDeliveries lists the webhook deliveries of an email's events, oldest
// first, after the cursor delivery.
func (v *Views) EmailDeliveries(ctx context.Context, id, cursor uuid.UUID, limit int) ([]store.AdminEmailDeliveriesRow, bool, error) {
	if err := checkLimit(limit); err != nil {
		return nil, false, err
	}
	e, err := v.Email(ctx, id)
	if err != nil {
		return nil, false, err
	}
	var rows []store.AdminEmailDeliveriesRow
	err = v.bounded(ctx, func(q *store.Queries) (err error) {
		rows, err = q.AdminEmailDeliveries(ctx, store.AdminEmailDeliveriesParams{
			EmailID: id, ProjectID: e.ProjectID, AfterID: cursor, RowLimit: int32(limit + 1), //nolint:gosec // bounded
		})
		return err
	})
	if errors.Is(err, ErrViewBusy) {
		return nil, false, err
	}
	if err != nil {
		return nil, false, fmt.Errorf("list deliveries: %w", err)
	}
	p, more := page(rows, limit)
	return p, more, nil
}

// Templates lists templates, newest first, optionally of one project.
func (v *Views) Templates(ctx context.Context, project *uuid.UUID, cursor uuid.UUID, limit int) ([]store.Template, bool, error) {
	if err := checkLimit(limit); err != nil {
		return nil, false, err
	}
	rows, err := v.q().AdminListTemplates(ctx, store.AdminListTemplatesParams{
		BeforeID: before(cursor), ProjectID: project, RowLimit: int32(limit + 1), //nolint:gosec // bounded
	})
	if err != nil {
		return nil, false, fmt.Errorf("list templates: %w", err)
	}
	p, more := page(rows, limit)
	return p, more, nil
}

// TemplateVersionInfo names one version of a template.
type TemplateVersionInfo struct {
	Version   int
	CreatedAt time.Time
}

// Template returns a template of any project with all its versions, newest
// first.
func (v *Views) Template(ctx context.Context, id uuid.UUID) (store.Template, []TemplateVersionInfo, error) {
	q := v.q()
	t, err := q.AdminGetTemplate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Template{}, nil, ErrNotFound
	}
	if err != nil {
		return store.Template{}, nil, fmt.Errorf("get template: %w", err)
	}
	rows, err := q.ListTemplateVersions(ctx, store.ListTemplateVersionsParams{TemplateID: id, ProjectID: t.ProjectID})
	if err != nil {
		return store.Template{}, nil, fmt.Errorf("list template versions: %w", err)
	}
	versions := make([]TemplateVersionInfo, 0, len(rows))
	for _, r := range rows {
		versions = append(versions, TemplateVersionInfo{Version: int(r.Version), CreatedAt: r.CreatedAt})
	}
	return t, versions, nil
}

// TemplateVersion returns a version of a template of any project exactly as
// published.
func (v *Views) TemplateVersion(ctx context.Context, id uuid.UUID, version int) (templates.Version, error) {
	if version < 1 {
		return templates.Version{}, ErrNotFound
	}
	t, err := v.q().AdminGetTemplate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return templates.Version{}, ErrNotFound
	}
	if err != nil {
		return templates.Version{}, fmt.Errorf("get template: %w", err)
	}
	tv, err := templates.Resolve(ctx, store.New(v.pool), t.ProjectID, id, version)
	if errors.Is(err, templates.ErrNotFound) {
		return templates.Version{}, ErrNotFound
	}
	return tv, err
}

// Preview renders a version with sample variables. Nothing is stored or
// sent. Variable and output errors are *templates.Error.
func (v *Views) Preview(ctx context.Context, id uuid.UUID, version int, variables []byte) (templates.Rendered, error) {
	tv, err := v.TemplateVersion(ctx, id, version)
	if err != nil {
		return templates.Rendered{}, err
	}
	schema, err := templates.ParseSchema(tv.Schema)
	if err != nil {
		return templates.Rendered{}, fmt.Errorf("stored schema: %v", err) //nolint:errorlint // a stored version is server state
	}
	if err := templates.ValidateVariables(schema, variables); err != nil {
		return templates.Rendered{}, err
	}
	release, err := slot(ctx, v.previews)
	if err != nil {
		return templates.Rendered{}, err
	}
	defer release()
	c, err := tv.Compile()
	if err != nil {
		return templates.Rendered{}, fmt.Errorf("compile stored template: %v", err) //nolint:errorlint // a stored version is server state
	}
	return c.Render(variables)
}

// Senders lists sender identities, newest first, optionally of one project.
func (v *Views) Senders(ctx context.Context, project *uuid.UUID, cursor uuid.UUID, limit int) ([]store.SenderIdentity, bool, error) {
	if err := checkLimit(limit); err != nil {
		return nil, false, err
	}
	rows, err := v.q().AdminListSenders(ctx, store.AdminListSendersParams{
		BeforeID: before(cursor), ProjectID: project, RowLimit: int32(limit + 1), //nolint:gosec // bounded
	})
	if err != nil {
		return nil, false, fmt.Errorf("list senders: %w", err)
	}
	p, more := page(rows, limit)
	return p, more, nil
}

// WebhookEndpoints lists webhook endpoints (never their secrets), newest
// first, optionally of one project.
func (v *Views) WebhookEndpoints(ctx context.Context, project *uuid.UUID, cursor uuid.UUID, limit int) ([]store.AdminListWebhookEndpointsRow, bool, error) {
	if err := checkLimit(limit); err != nil {
		return nil, false, err
	}
	rows, err := v.q().AdminListWebhookEndpoints(ctx, store.AdminListWebhookEndpointsParams{
		BeforeID: before(cursor), ProjectID: project, RowLimit: int32(limit + 1), //nolint:gosec // bounded
	})
	if err != nil {
		return nil, false, fmt.Errorf("list webhook endpoints: %w", err)
	}
	p, more := page(rows, limit)
	return p, more, nil
}
