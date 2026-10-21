//go:build integration

package broadcasts_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
	"github.com/skraus2/email-platform/internal/testdb"
)

type fixture struct {
	pool      *pgxpool.Pool
	project   uuid.UUID
	other     uuid.UUID
	audience  uuid.UUID
	template  uuid.UUID
	store     *broadcasts.Store
	templates *templates.Store
	ctx       context.Context
}

const goodSchema = `{"unsubscribe_url":{"type":"string","required":true},"name":{"type":"string"},"plan":{"type":"string"}}`

var goodSource = templates.Source{
	Subject: "News for {{.name}}",
	Text:    "Hi {{.name}}\nUnsubscribe: {{.unsubscribe_url}}",
	HTML:    `<p>Hi {{.name}}</p><a href="{{.unsubscribe_url}}">Unsubscribe</a>`,
}

func newFixture(t *testing.T, maxOpen int) *fixture {
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
	au, err := contacts.NewAudiences(pool, 10, 1000).Create(ctx, a.ID, "list")
	if err != nil {
		t.Fatal(err)
	}
	ts := templates.NewStore(pool, 100, 100, 50<<20, 1_000_000)
	tmpl, err := ts.Create(ctx, a.ID, "news", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Publish(ctx, a.ID, tmpl.ID, goodSource, []byte(goodSchema)); err != nil {
		t.Fatal(err)
	}
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), 5)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		pool: pool, project: a.ID, other: b.ID, audience: au.ID, template: tmpl.ID,
		store: broadcasts.NewStore(pool, enq, maxOpen), templates: ts, ctx: ctx,
	}
}

func (f *fixture) input() broadcasts.CreateInput {
	return broadcasts.CreateInput{
		Name: "October", AudienceID: f.audience, From: "news@example.test", TemplateID: f.template,
		Variables: json.RawMessage(`{"plan":"pro"}`), UnsubscribeURL: "https://app.example.test/unsubscribe?t={token}",
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

func TestCreateBroadcastDraft(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	b, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != broadcasts.StatusDraft || b.TemplateVersion != 1 || b.Name != "October" || b.From != "news@example.test" ||
		b.AudienceID != f.audience || b.ScheduledAt != nil || b.ProjectID != f.project {
		t.Fatalf("draft: %+v", b)
	}
	// "published" resolves to the highest version at create time.
	if _, err := f.templates.Publish(f.ctx, f.project, f.template, goodSource, []byte(goodSchema)); err != nil {
		t.Fatal(err)
	}
	b2, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil || b2.TemplateVersion != 2 {
		t.Fatalf("published: %+v %v", b2, err)
	}
	in := f.input()
	in.TemplateVersion = 1
	if b3, err := f.store.Create(f.ctx, f.project, in); err != nil || b3.TemplateVersion != 1 {
		t.Fatalf("explicit version: %+v %v", b3, err)
	}
	got, err := f.store.Get(f.ctx, f.project, b.ID)
	if err != nil || got.ID != b.ID || got.Counts.Recipients != 0 || len(got.Counts.ByStatus) != 9 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := f.store.Get(f.ctx, f.other, b.ID); !errors.Is(err, broadcasts.ErrNotFound) {
		t.Fatalf("foreign get: %v", err)
	}
}

func TestCreateBroadcastRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	foreignAudience, err := contacts.NewAudiences(f.pool, 10, 10).Create(f.ctx, f.other, "theirs")
	if err != nil {
		t.Fatal(err)
	}
	publish := func(src templates.Source, schema string) uuid.UUID {
		t.Helper()
		tmpl, err := f.templates.Create(f.ctx, f.project, "t"+uuid.NewString()[:8], "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.templates.Publish(f.ctx, f.project, tmpl.ID, src, []byte(schema)); err != nil {
			t.Fatal(err)
		}
		return tmpl.ID
	}
	noField := publish(templates.Source{Subject: "s", Text: "no link"}, `{"name":{"type":"string"}}`)
	numberField := publish(templates.Source{Subject: "s", Text: "{{.unsubscribe_url}}"}, `{"unsubscribe_url":{"type":"number"}}`)
	onlyInIf := publish(templates.Source{Subject: "s", Text: "{{if .vip}}{{.unsubscribe_url}}{{end}}"},
		`{"unsubscribe_url":{"type":"string"},"vip":{"type":"boolean"}}`)
	htmlMissing := publish(templates.Source{Subject: "s", Text: "{{.unsubscribe_url}}", HTML: "<p>hi</p>"}, `{"unsubscribe_url":{"type":"string"}}`)
	empty, err := f.templates.Create(f.ctx, f.project, "empty", "")
	if err != nil {
		t.Fatal(err)
	}

	var ve *broadcasts.Error
	var te *templates.Error
	for name, c := range map[string]struct {
		change func(*broadcasts.CreateInput)
		check  func(error) bool
	}{
		"foreign audience":        {func(in *broadcasts.CreateInput) { in.AudienceID = foreignAudience.ID }, func(err error) bool { return errors.Is(err, broadcasts.ErrAudienceNotFound) }},
		"unknown audience":        {func(in *broadcasts.CreateInput) { in.AudienceID = uuid.New() }, func(err error) bool { return errors.Is(err, broadcasts.ErrAudienceNotFound) }},
		"unknown template":        {func(in *broadcasts.CreateInput) { in.TemplateID = uuid.New() }, func(err error) bool { return errors.Is(err, broadcasts.ErrTemplateNotFound) }},
		"unknown version":         {func(in *broadcasts.CreateInput) { in.TemplateVersion = 9 }, func(err error) bool { return errors.Is(err, broadcasts.ErrTemplateNotFound) }},
		"no versions":             {func(in *broadcasts.CreateInput) { in.TemplateID = empty.ID }, func(err error) bool { return errors.Is(err, broadcasts.ErrTemplateNotFound) }},
		"no unsubscribe variable": {func(in *broadcasts.CreateInput) { in.TemplateID, in.Variables = noField, nil }, func(err error) bool { return errors.Is(err, broadcasts.ErrMissingUnsubscribe) }},
		"number variable":         {func(in *broadcasts.CreateInput) { in.TemplateID, in.Variables = numberField, nil }, func(err error) bool { return errors.Is(err, broadcasts.ErrMissingUnsubscribe) }},
		"only inside if":          {func(in *broadcasts.CreateInput) { in.TemplateID, in.Variables = onlyInIf, nil }, func(err error) bool { return errors.Is(err, broadcasts.ErrMissingUnsubscribe) }},
		"html without link":       {func(in *broadcasts.CreateInput) { in.TemplateID, in.Variables = htmlMissing, nil }, func(err error) bool { return errors.Is(err, broadcasts.ErrMissingUnsubscribe) }},
		"bad from":                {func(in *broadcasts.CreateInput) { in.From = "News <n@example.test>" }, func(err error) bool { return errors.As(err, &ve) && ve.Field == "from" }},
		"long name":               {func(in *broadcasts.CreateInput) { in.Name = strings.Repeat("n", 201) }, func(err error) bool { return errors.As(err, &ve) && ve.Field == "name" }},
		"no token":                {func(in *broadcasts.CreateInput) { in.UnsubscribeURL = "https://app.example.test/u" }, func(err error) bool { return errors.As(err, &ve) && ve.Field == "unsubscribe_url" }},
		"two tokens":              {func(in *broadcasts.CreateInput) { in.UnsubscribeURL = "https://app.example.test/{token}/{token}" }, func(err error) bool { return errors.As(err, &ve) && ve.Field == "unsubscribe_url" }},
		"http":                    {func(in *broadcasts.CreateInput) { in.UnsubscribeURL = "http://app.example.test/u?t={token}" }, func(err error) bool { return errors.As(err, &ve) && ve.Field == "unsubscribe_url" }},
		"comma":                   {func(in *broadcasts.CreateInput) { in.UnsubscribeURL = "https://app.example.test/u?t={token},x" }, func(err error) bool { return errors.As(err, &ve) && ve.Field == "unsubscribe_url" }},
		"too long": {func(in *broadcasts.CreateInput) {
			in.UnsubscribeURL = "https://a.test/{token}" + strings.Repeat("x", 900)
		}, func(err error) bool { return errors.As(err, &ve) && ve.Field == "unsubscribe_url" }},
		"reserved variable":   {func(in *broadcasts.CreateInput) { in.Variables = json.RawMessage(`{"unsubscribe_url":"x"}`) }, func(err error) bool { return errors.As(err, &ve) && ve.Field == "variables" }},
		"undeclared variable": {func(in *broadcasts.CreateInput) { in.Variables = json.RawMessage(`{"other":"x"}`) }, func(err error) bool { return errors.As(err, &te) && te.Code == templates.CodeVariablesInvalid }},
		"wrong type":          {func(in *broadcasts.CreateInput) { in.Variables = json.RawMessage(`{"plan":1}`) }, func(err error) bool { return errors.As(err, &te) && te.Code == templates.CodeVariablesInvalid }},
		"huge variables": {func(in *broadcasts.CreateInput) {
			in.Variables = json.RawMessage(`{"plan":"` + strings.Repeat("x", 17<<10) + `"}`)
		}, func(err error) bool { return errors.As(err, &ve) && ve.Field == "variables" }},
	} {
		in := f.input()
		c.change(&in)
		if _, err := f.store.Create(f.ctx, f.project, in); !c.check(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := f.count(t, "SELECT count(*) FROM broadcasts"); n != 0 {
		t.Fatalf("refusals stored %d broadcasts", n)
	}

	// Verified senders, when required.
	f.store.RequireVerifiedSenders()
	if _, err := f.store.Create(f.ctx, f.project, f.input()); !errors.Is(err, emails.ErrSenderNotVerified) {
		t.Fatalf("unverified sender: %v", err)
	}
}

func TestOpenBroadcastCap(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 3)
	var ids []uuid.UUID
	for range 2 {
		b, err := f.store.Create(f.ctx, f.project, f.input())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Go(func() { _, errs[i] = f.store.Create(f.ctx, f.project, f.input()) })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, broadcasts.ErrLimit):
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("created %d at the cap", ok)
	}
	// A cancelled broadcast no longer counts.
	if _, err := f.store.Cancel(f.ctx, f.project, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input()); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
}

func TestScheduleAndCancelBroadcast(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	b, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Schedule(f.ctx, f.other, b.ID, nil); !errors.Is(err, broadcasts.ErrNotFound) {
		t.Fatalf("foreign schedule: %v", err)
	}
	far := time.Now().Add(broadcasts.DefaultScheduleMaxAhead + time.Minute)
	var ve *broadcasts.Error
	if _, err := f.store.Schedule(f.ctx, f.project, b.ID, &far); !errors.As(err, &ve) || ve.Field != "scheduled_at" {
		t.Fatalf("beyond horizon: %v", err)
	}
	at := time.Now().Add(time.Hour)
	s, err := f.store.Schedule(f.ctx, f.project, b.ID, &at)
	if err != nil || s.Status != broadcasts.StatusScheduled || s.ScheduledAt == nil || s.ScheduledAt.Sub(at).Abs() > time.Millisecond {
		t.Fatalf("schedule: %+v %v", s, err)
	}
	if n := f.count(t, "SELECT count(*) FROM river_job WHERE kind = 'broadcast_fanout' AND queue = 'broadcasts' AND state = 'scheduled' AND scheduled_at > now() + interval '59 minutes'"); n != 1 {
		t.Fatalf("fan-out jobs: %d", n)
	}
	if _, err := f.store.Schedule(f.ctx, f.project, b.ID, nil); !errors.Is(err, broadcasts.ErrNotDraft) {
		t.Fatalf("second schedule: %v", err)
	}

	// Without a time (or a past one) it starts now.
	b2, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	s2, err := f.store.Schedule(f.ctx, f.project, b2.ID, &past)
	if err != nil || s2.ScheduledAt == nil || time.Since(*s2.ScheduledAt) > time.Minute {
		t.Fatalf("schedule now: %+v %v", s2, err)
	}

	if _, err := f.store.Cancel(f.ctx, f.other, b.ID); !errors.Is(err, broadcasts.ErrNotFound) {
		t.Fatalf("foreign cancel: %v", err)
	}
	c, err := f.store.Cancel(f.ctx, f.project, b.ID)
	if err != nil || c.Status != broadcasts.StatusCanceled || c.FinishedAt == nil {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	if _, err := f.store.Cancel(f.ctx, f.project, b.ID); !errors.Is(err, broadcasts.ErrNotCancelable) {
		t.Fatalf("second cancel: %v", err)
	}
	if _, err := f.store.Schedule(f.ctx, f.project, b.ID, nil); !errors.Is(err, broadcasts.ErrNotDraft) {
		t.Fatalf("schedule cancelled: %v", err)
	}
	// A completed broadcast can still be cancelled (stops queued emails).
	if _, err := f.pool.Exec(f.ctx, "UPDATE broadcasts SET status = 'completed', finished_at = now() WHERE id = $1", b2.ID); err != nil {
		t.Fatal(err)
	}
	if c, err := f.store.Cancel(f.ctx, f.project, b2.ID); err != nil || c.Status != broadcasts.StatusCanceled {
		t.Fatalf("cancel completed: %+v %v", c, err)
	}
	// A draft is cancelled without ever being scheduled.
	b3, _ := f.store.Create(f.ctx, f.project, f.input())
	if c, err := f.store.Cancel(f.ctx, f.project, b3.ID); err != nil || c.Status != broadcasts.StatusCanceled {
		t.Fatalf("cancel draft: %+v %v", c, err)
	}
}

func TestBroadcastCountsAndList(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	b, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"queued", "queued", "captured", "failed"} {
		finished, code := "NULL", "NULL"
		if st != "queued" {
			finished = "now()"
		}
		if st == "failed" {
			code = "'unsubscribed'"
		}
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO emails (project_id, sender, recipient, subject, status, finished_at, error_code, broadcast_id, template_id, template_version)
			VALUES ($1, 'news@example.test', 'r@example.test', 's', $2, `+finished+`, `+code+`, $3, $4, 1)`, f.project, st, b.ID, f.template); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE broadcasts SET recipients = 4, skipped_unsubscribed = 2 WHERE id = $1", b.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.Get(f.ctx, f.project, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Counts
	if c.Recipients != 4 || c.SkippedUnsubscribed != 2 || c.ByStatus["queued"] != 2 || c.ByStatus["captured"] != 1 ||
		c.ByStatus["failed"] != 1 || c.ByStatus["canceled"] != 0 || len(c.ByStatus) != 9 {
		t.Fatalf("counts: %+v", c)
	}

	for range 2 {
		if _, err := f.store.Create(f.ctx, f.project, f.input()); err != nil {
			t.Fatal(err)
		}
	}
	page, more, err := f.store.List(f.ctx, f.project, "", uuid.Nil, 2)
	if err != nil || !more || len(page) != 2 || page[0].Counts != nil {
		t.Fatalf("list: %d %v %v", len(page), more, err)
	}
	rest, more, err := f.store.List(f.ctx, f.project, "", page[1].ID, 2)
	if err != nil || more || len(rest) != 1 || rest[0].ID != b.ID {
		t.Fatalf("list rest: %+v %v %v", rest, more, err)
	}
	if page, _, _ := f.store.List(f.ctx, f.other, "", uuid.Nil, 10); len(page) != 0 {
		t.Fatal("foreign list")
	}
	if page, _, _ := f.store.List(f.ctx, f.project, broadcasts.StatusScheduled, uuid.Nil, 10); len(page) != 0 {
		t.Fatal("status filter")
	}
}

func TestBroadcastCreateRateAndVersionChecks(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	f.store.LimitCreates(2)
	for range 2 {
		if _, err := f.store.Create(f.ctx, f.project, f.input()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input()); !errors.Is(err, broadcasts.ErrRateLimited) {
		t.Fatalf("third create in an hour: %v", err)
	}
	// Cancelled broadcasts still count: create/cancel loops are limited.
	page, _, _ := f.store.List(f.ctx, f.project, "", uuid.Nil, 10)
	for _, b := range page {
		if _, err := f.store.Cancel(f.ctx, f.project, b.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input()); !errors.Is(err, broadcasts.ErrRateLimited) {
		t.Fatalf("after cancelling: %v", err)
	}

	g := newFixture(t, 20)
	// A max_length too short for the URL with its token.
	tmpl, err := g.templates.Create(g.ctx, g.project, "short", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.templates.Publish(g.ctx, g.project, tmpl.ID, templates.Source{Subject: "s", Text: "{{.unsubscribe_url}}"},
		[]byte(`{"unsubscribe_url":{"type":"string","max_length":60}}`)); err != nil {
		t.Fatal(err)
	}
	in := g.input()
	in.TemplateID, in.Variables = tmpl.ID, nil
	var ve *broadcasts.Error
	if _, err := g.store.Create(g.ctx, g.project, in); !errors.As(err, &ve) || ve.Field != "unsubscribe_url" {
		t.Fatalf("short max_length: %v", err)
	}
	// A template of another project is the same as an unknown one.
	ts := templates.NewStore(g.pool, 100, 100, 50<<20, 1_000_000)
	foreign, err := ts.Create(g.ctx, g.other, "theirs", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Publish(g.ctx, g.other, foreign.ID, goodSource, []byte(goodSchema)); err != nil {
		t.Fatal(err)
	}
	in = g.input()
	in.TemplateID = foreign.ID
	if _, err := g.store.Create(g.ctx, g.project, in); !errors.Is(err, broadcasts.ErrTemplateNotFound) {
		t.Fatalf("foreign template: %v", err)
	}
	in.TemplateVersion = 1
	if _, err := g.store.Create(g.ctx, g.project, in); !errors.Is(err, broadcasts.ErrTemplateNotFound) {
		t.Fatalf("foreign template version: %v", err)
	}
}

func TestScheduleRechecksSender(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	b, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	f.store.RequireVerifiedSenders()
	if _, err := f.store.Schedule(f.ctx, f.project, b.ID, nil); !errors.Is(err, emails.ErrSenderNotVerified) {
		t.Fatalf("unverified sender: %v", err)
	}
	got, _ := f.store.Get(f.ctx, f.project, b.ID)
	if got.Status != broadcasts.StatusDraft || f.count(t, "SELECT count(*) FROM river_job WHERE kind = 'broadcast_fanout'") != 0 {
		t.Fatalf("refused schedule left %s and a job", got.Status)
	}
	if _, err := f.pool.Exec(f.ctx, "INSERT INTO sender_identities (project_id, kind, value, status, verified_at) VALUES ($1, 'domain', 'example.test', 'verified', now())", f.project); err != nil {
		t.Fatal(err)
	}
	s, err := f.store.Schedule(f.ctx, f.project, b.ID, nil)
	if err != nil || s.Status != broadcasts.StatusScheduled {
		t.Fatalf("verified sender: %+v %v", s, err)
	}
	// Starting now: the fan-out job is available at once.
	if n := f.count(t, "SELECT count(*) FROM river_job WHERE kind = 'broadcast_fanout' AND state = 'available'"); n != 1 {
		t.Fatalf("available fan-out jobs: %d", n)
	}
}

func TestOneEmailPerContactAndBroadcast(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	b, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	src := "x"
	c, err := contacts.NewStore(f.pool, 10).Create(f.ctx, f.project, contacts.CreateInput{Email: "a@example.test", Status: "subscribed", ConsentSource: &src})
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO emails (project_id, sender, recipient, subject, broadcast_id, contact_id) VALUES ($1, 'n@example.test', 'a@example.test', 's', $2, $3)`
	if _, err := f.pool.Exec(f.ctx, insert, f.project, b.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(f.ctx, insert, f.project, b.ID, c.ID)
	if pgErr := (*pgconn.PgError)(nil); !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second email for the same contact: %v", err)
	}
}
