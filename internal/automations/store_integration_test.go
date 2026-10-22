//go:build integration

package automations_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
	"github.com/skraus2/email-platform/internal/testdb"
)

type fixture struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	project   uuid.UUID
	other     uuid.UUID
	template  uuid.UUID
	foreign   uuid.UUID // a template of the other project
	templates *templates.Store
	enq       *jobs.Enqueuer
	store     *automations.Store
}

const goodSchema = `{"unsubscribe_url":{"type":"string","required":true},"name":{"type":"string"},"plan":{"type":"string"},"order":{"type":"string"}}`

var goodSource = templates.Source{
	Subject: "Hello {{.name}}",
	Text:    "Hi {{.name}} {{.order}}\nUnsubscribe: {{.unsubscribe_url}}",
	HTML:    `<p>Hi {{.name}} {{.order}}</p><a href="{{.unsubscribe_url}}">Unsubscribe</a>`,
}

func newFixture(t *testing.T, maxPerProject int) *fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	a, err := auth.CreateProject(ctx, q, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.CreateProject(ctx, q, "b")
	if err != nil {
		t.Fatal(err)
	}
	ts := templates.NewStore(pool, 100, 100, 50<<20, 1_000_000)
	publish := func(project uuid.UUID, src templates.Source, schema string) uuid.UUID {
		tmpl, err := ts.Create(ctx, project, "t"+uuid.NewString()[:8], "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ts.Publish(ctx, project, tmpl.ID, src, []byte(schema)); err != nil {
			t.Fatal(err)
		}
		return tmpl.ID
	}
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), 5)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		ctx: ctx, pool: pool, project: a.ID, other: b.ID,
		template: publish(a.ID, goodSource, goodSchema), foreign: publish(b.ID, goodSource, goodSchema),
		templates: ts, enq: enq, store: automations.NewStore(pool, enq, maxPerProject),
	}
}

func (f *fixture) sendStep(template uuid.UUID, version string) string {
	return fmt.Sprintf(`{"type":"send","from":"news@example.test","template_id":%q,"template_version":%s,"variables":{"plan":"pro"},"unsubscribe_url":"https://app.example.test/u?t={token}"}`, template, version)
}

func (f *fixture) input(steps ...string) automations.CreateInput {
	if len(steps) == 0 {
		steps = []string{f.sendStep(f.template, `"published"`), `{"type":"wait","seconds":3600}`, f.sendStep(f.template, "1")}
	}
	return automations.CreateInput{
		Name: "Welcome", TriggerEvent: "user.signed_up", Steps: json.RawMessage("[" + strings.Join(steps, ",") + "]"),
	}
}

func (f *fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateAutomation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	a, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.Status != automations.StatusActive || a.TriggerEvent != "user.signed_up" || a.AllowReentry || len(a.Steps) != 3 {
		t.Fatalf("unexpected automation: %+v", a)
	}
	// "published" is resolved and stored as the number.
	if a.Steps[0].TemplateVersion != 1 || string(a.Steps[0].Variables) != `{"plan":"pro"}` {
		t.Fatalf("send step not resolved: %+v", a.Steps[0])
	}
	got, err := f.store.Get(f.ctx, f.project, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 3 || got.Steps[1].Seconds != 3600 || got.RunCounts[automations.RunActive] != 0 || len(got.RunCounts) != 5 {
		t.Fatalf("Get: %+v", got)
	}
	// A new published version does not change the stored step.
	if _, err := f.templates.Publish(f.ctx, f.project, f.template, goodSource, []byte(goodSchema)); err != nil {
		t.Fatal(err)
	}
	got, _ = f.store.Get(f.ctx, f.project, a.ID)
	if got.Steps[0].TemplateVersion != 1 {
		t.Fatalf("stored version moved: %d", got.Steps[0].TemplateVersion)
	}
	paused := f.input()
	paused.Status = automations.StatusPaused
	paused.AllowReentry = true
	p, err := f.store.Create(f.ctx, f.project, paused)
	if err != nil || p.Status != automations.StatusPaused || !p.AllowReentry {
		t.Fatalf("paused create: %+v %v", p, err)
	}
}

func TestCreateAutomationRefuses(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	noLink := func() uuid.UUID {
		tmpl, err := f.templates.Create(f.ctx, f.project, "nolink", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.templates.Publish(f.ctx, f.project, tmpl.ID, templates.Source{Subject: "s", Text: "no link"}, []byte(`{"unsubscribe_url":{"type":"string"}}`)); err != nil {
			t.Fatal(err)
		}
		return tmpl.ID
	}()
	cases := map[string]struct {
		in    func() automations.CreateInput
		want  error
		field string
	}{
		"empty name":      {func() automations.CreateInput { in := f.input(); in.Name = ""; return in }, nil, "name"},
		"bad trigger":     {func() automations.CreateInput { in := f.input(); in.TriggerEvent = "Signup"; return in }, nil, "trigger.event"},
		"archived status": {func() automations.CreateInput { in := f.input(); in.Status = "archived"; return in }, nil, "status"},
		"bad step": {func() automations.CreateInput {
			return f.input(`{"type":"wait","seconds":1}`, f.sendStep(f.template, "1"))
		}, nil, "steps[0].seconds"},
		"foreign template": {func() automations.CreateInput { return f.input(f.sendStep(f.foreign, "1")) }, automations.ErrTemplateNotFound, ""},
		"unknown version":  {func() automations.CreateInput { return f.input(f.sendStep(f.template, "9")) }, automations.ErrTemplateNotFound, ""},
		"no unsubscribe":   {func() automations.CreateInput { return f.input(f.sendStep(noLink, "1")) }, automations.ErrMissingUnsubscribe, ""},
		"bad from": {func() automations.CreateInput {
			return f.input(strings.Replace(f.sendStep(f.template, "1"), "news@example.test", "a b", 1))
		}, nil, "steps[0].from"},
		"bad url": {func() automations.CreateInput {
			return f.input(strings.Replace(f.sendStep(f.template, "1"), "{token}", "x", 1))
		}, nil, "steps[0].unsubscribe_url"},
		"reserved variable": {func() automations.CreateInput {
			return f.input(strings.Replace(f.sendStep(f.template, "1"), `{"plan":"pro"}`, `{"unsubscribe_url":"x"}`, 1))
		}, nil, "steps[0].variables"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.store.Create(f.ctx, f.project, tc.in())
			if tc.want != nil {
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
				return
			}
			var e *automations.Error
			if !errors.As(err, &e) || e.Field != tc.field {
				t.Fatalf("got %v, want field %s", err, tc.field)
			}
		})
	}
	// Undeclared shared variable: a template error naming the step.
	_, err := f.store.Create(f.ctx, f.project, f.input(strings.Replace(f.sendStep(f.template, "1"), `{"plan":"pro"}`, `{"nope":"x"}`, 1)))
	var te *templates.Error
	if !errors.As(err, &te) || !strings.HasPrefix(te.Field, "steps[0].variables") {
		t.Fatalf("undeclared variable: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM automations`); n != 0 {
		t.Fatalf("%d automations stored by refused creates", n)
	}
}

func TestCreateAutomationRequiresVerifiedSender(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	f.store.RequireVerifiedSenders()
	if _, err := f.store.Create(f.ctx, f.project, f.input()); !errors.Is(err, emails.ErrSenderNotVerified) {
		t.Fatalf("got %v", err)
	}
	q := store.New(f.pool)
	id, err := emails.AddSender(f.ctx, q, f.project, emails.SenderDomain, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.VerifySender(f.ctx, q, f.project, id.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input()); err != nil {
		t.Fatalf("verified sender refused: %v", err)
	}
}

func TestAutomationCapConcurrent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 3)
	for range 2 {
		if _, err := f.store.Create(f.ctx, f.project, f.input()); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := f.store.Create(f.ctx, f.project, f.input())
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok, limited := 0, 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, automations.ErrLimit):
			limited++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 || limited != 7 {
		t.Fatalf("ok=%d limited=%d, want 1 and 7", ok, limited)
	}
	// Archived automations do not count; the other project has its own cap.
	page, _, err := f.store.List(f.ctx, f.project, "", uuid.Nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	archived := automations.StatusArchived
	if _, err := f.store.Update(f.ctx, f.project, page[0].ID, automations.UpdateInput{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input()); err != nil {
		t.Fatalf("create after archive: %v", err)
	}
}

func TestUpdateAutomation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	a, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	name, paused, active, archived, bogus := "Renamed", automations.StatusPaused, automations.StatusActive, automations.StatusArchived, "draft"
	u, err := f.store.Update(f.ctx, f.project, a.ID, automations.UpdateInput{Name: &name, Status: &paused})
	if err != nil || u.Name != name || u.Status != paused || len(u.Steps) != 3 {
		t.Fatalf("update: %+v %v", u, err)
	}
	if u, err = f.store.Update(f.ctx, f.project, a.ID, automations.UpdateInput{Status: &active}); err != nil || u.Status != active {
		t.Fatalf("reactivate: %+v %v", u, err)
	}
	var e *automations.Error
	if _, err := f.store.Update(f.ctx, f.project, a.ID, automations.UpdateInput{Status: &bogus}); !errors.As(err, &e) {
		t.Fatalf("bad status: %v", err)
	}
	if _, err := f.store.Update(f.ctx, f.project, a.ID, automations.UpdateInput{}); !errors.As(err, &e) {
		t.Fatalf("empty update: %v", err)
	}
	// Foreign and unknown ids look the same.
	for _, id := range []uuid.UUID{uuid.New(), a.ID} {
		project := f.project
		if id == a.ID {
			project = f.other
		}
		if _, err := f.store.Update(f.ctx, project, id, automations.UpdateInput{Name: &name}); !errors.Is(err, automations.ErrNotFound) {
			t.Fatalf("foreign/unknown update: %v", err)
		}
		if _, err := f.store.Get(f.ctx, project, id); !errors.Is(err, automations.ErrNotFound) {
			t.Fatalf("foreign/unknown get: %v", err)
		}
	}
	// Archiving enqueues the run cancellation job in the same transaction
	// and is final.
	if u, err = f.store.Update(f.ctx, f.project, a.ID, automations.UpdateInput{Status: &archived}); err != nil || u.Status != archived {
		t.Fatalf("archive: %+v %v", u, err)
	}
	if n := f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'automation_archive' AND args->>'automation_id' = $1`, a.ID.String()); n != 1 {
		t.Fatalf("%d archive jobs", n)
	}
	for _, in := range []automations.UpdateInput{{Status: &active}, {Name: &name}, {Status: &archived}} {
		if _, err := f.store.Update(f.ctx, f.project, a.ID, in); !errors.Is(err, automations.ErrArchived) {
			t.Fatalf("change after archive: %v", err)
		}
	}
}

func TestListAutomations(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	var ids []uuid.UUID
	for range 5 {
		a, err := f.store.Create(f.ctx, f.project, f.input())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	if _, err := f.store.Create(f.ctx, f.other, automations.CreateInput{
		Name: "x", TriggerEvent: "e", Steps: json.RawMessage("[" + f.sendStep(f.foreign, "1") + "]"),
	}); err != nil {
		t.Fatal(err)
	}
	paused := automations.StatusPaused
	if _, err := f.store.Update(f.ctx, f.project, ids[1], automations.UpdateInput{Status: &paused}); err != nil {
		t.Fatal(err)
	}
	page, more, err := f.store.List(f.ctx, f.project, "", uuid.Nil, 3)
	if err != nil || !more || len(page) != 3 || page[0].ID != ids[4] {
		t.Fatalf("page 1: %d %v %v", len(page), more, err)
	}
	page, more, err = f.store.List(f.ctx, f.project, "", page[2].ID, 3)
	if err != nil || more || len(page) != 2 || page[1].ID != ids[0] {
		t.Fatalf("page 2: %d %v %v", len(page), more, err)
	}
	page, _, err = f.store.List(f.ctx, f.project, automations.StatusPaused, uuid.Nil, 10)
	if err != nil || len(page) != 1 || page[0].ID != ids[1] {
		t.Fatalf("status filter: %v %v", page, err)
	}
	page, _, _ = f.store.List(f.ctx, f.other, "", uuid.Nil, 10)
	if len(page) != 1 {
		t.Fatalf("other project sees %d", len(page))
	}
}
