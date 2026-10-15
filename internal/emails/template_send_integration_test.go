//go:build integration

package emails_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
)

func publishTemplate(t *testing.T, pool *pgxpool.Pool, projectID uuid.UUID, versions ...templates.Source) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	s := templates.NewStore(pool, 200, 500, 50<<20, 1_000_000)
	tmpl, err := s.Create(ctx, projectID, "t-"+uuid.NewString()[:8], "")
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range versions {
		schema := `{"name":{"type":"string","required":true}}`
		if src.Subject == "v2" {
			schema = `{"name":{"type":"string","required":true},"plan":{"type":"string","required":true}}`
		}
		if _, err := s.Publish(ctx, projectID, tmpl.ID, src, []byte(schema)); err != nil {
			t.Fatal(err)
		}
	}
	return tmpl.ID
}

var (
	v1Source = templates.Source{Subject: "Hi {{.name}}", Text: "Hello {{.name}}", HTML: "<p>Hello {{.name}}</p>"}
	v2Source = templates.Source{Subject: "v2", Text: "{{.name}} on {{.plan}}"}
)

func templateInput(id uuid.UUID, version int, vars string) emails.CreateInput {
	return emails.CreateInput{From: "noreply@example.test", To: "customer@example.test",
		TemplateID: id, TemplateVersion: version, Variables: []byte(vars)}
}

func TestTemplateSendStoresRenderedContentAndReference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	var logs bytes.Buffer
	svc := newService(t, pool, &logs)
	id := publishTemplate(t, pool, projectID, v1Source)

	e, err := svc.Create(ctx, projectID, templateInput(id, 0, `{"name":"<Ada>"}`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Subject != "Hi <Ada>" || e.TemplateID == nil || *e.TemplateID != id || e.TemplateVersion == nil || *e.TemplateVersion != 1 {
		t.Fatalf("email %+v", e)
	}
	var text, html string
	if err := pool.QueryRow(ctx, "SELECT text_body, html_body FROM email_contents WHERE email_id = $1", e.ID).Scan(&text, &html); err != nil {
		t.Fatal(err)
	}
	if text != "Hello <Ada>" || html != "<p>Hello &lt;Ada&gt;</p>" {
		t.Fatalf("content %q %q", text, html)
	}
	got, err := svc.Get(ctx, projectID, e.ID)
	if err != nil || got.TemplateID == nil || *got.TemplateVersion != 1 {
		t.Fatalf("get %+v %v", got, err)
	}
	// Variable values never reach the logs.
	if _, err := svc.Create(ctx, projectID, templateInput(id, 0, `{"name":"Zq9-secret-value"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, projectID, templateInput(id, 0, `{"name":1,"zz-secret-key":"Zq9-secret-value"}`)); err == nil {
		t.Fatal("invalid variables accepted")
	}
	if strings.Contains(logs.String(), "Zq9-secret") || strings.Contains(logs.String(), "zz-secret-key") {
		t.Fatalf("variables in logs: %s", logs.String())
	}
}

func TestTemplateSendResolvesPublishedAndExplicitVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	id := publishTemplate(t, pool, projectID, v1Source, v2Source)

	latest, err := svc.Create(ctx, projectID, templateInput(id, 0, `{"name":"Ada","plan":"pro"}`))
	if err != nil || *latest.TemplateVersion != 2 || latest.Subject != "v2" {
		t.Fatalf("published: %+v %v", latest, err)
	}
	old, err := svc.Create(ctx, projectID, templateInput(id, 1, `{"name":"Ada"}`))
	if err != nil || *old.TemplateVersion != 1 || old.Subject != "Hi Ada" {
		t.Fatalf("version 1: %+v %v", old, err)
	}
}

func TestTemplateSendRefusalsStoreNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	other, err := auth.CreateProject(ctx, store.New(pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, pool, nil)
	id := publishTemplate(t, pool, projectID, v1Source)
	foreign := publishTemplate(t, pool, other.ID, v1Source)
	empty := publishTemplate(t, pool, projectID)

	for name, tc := range map[string]struct {
		in   emails.CreateInput
		want func(error) bool
	}{
		"unknown template":   {templateInput(uuid.New(), 0, `{"name":"a"}`), isNotFound},
		"foreign template":   {templateInput(foreign, 1, `{"name":"a"}`), isNotFound},
		"unknown version":    {templateInput(id, 2, `{"name":"a"}`), isNotFound},
		"no versions":        {templateInput(empty, 0, `{"name":"a"}`), isNotFound},
		"missing required":   {templateInput(id, 0, `{}`), isCode(templates.CodeVariablesInvalid)},
		"undeclared":         {templateInput(id, 0, `{"name":"a","x":1}`), isCode(templates.CodeVariablesInvalid)},
		"wrong type":         {templateInput(id, 0, `{"name":1}`), isCode(templates.CodeVariablesInvalid)},
		"subject line break": {templateInput(id, 0, `{"name":"a\r\nBcc: x@example.test"}`), isCode(templates.CodeRenderFailed)},
	} {
		if _, err := svc.Create(ctx, projectID, tc.in); !tc.want(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c := counts(t, pool)
	if c.emails != 0 || c.contents != 0 || c.jobs != 0 {
		t.Fatalf("refusals stored something: %+v", c)
	}
	var windows int
	if err := pool.QueryRow(ctx, "SELECT coalesce(sum(count), 0) FROM rate_limit_windows").Scan(&windows); err != nil || windows != 0 {
		t.Fatalf("refusals counted against the rate limit: %d %v", windows, err)
	}
}

func isNotFound(err error) bool { return errors.Is(err, emails.ErrTemplateNotFound) }

func isCode(code string) func(error) bool {
	return func(err error) bool {
		var te *templates.Error
		return errors.As(err, &te) && te.Code == code
	}
}

func TestTemplateSendChecksSuppressionAndSender(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	svc.RequireVerifiedSenders()
	id := publishTemplate(t, pool, projectID, v1Source)
	// Variables that could not render: the refusal must come first.
	if _, err := svc.Create(ctx, projectID, templateInput(id, 0, `{}`)); !errors.Is(err, emails.ErrSenderNotVerified) {
		t.Fatalf("unverified sender: %v", err)
	}
	svc2 := newService(t, pool, nil)
	if _, err := emails.AddSuppression(ctx, store.New(pool), projectID, "customer@example.test", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.Create(ctx, projectID, templateInput(id, 0, `{}`)); !errors.Is(err, emails.ErrRecipientSuppressed) {
		t.Fatalf("suppressed: %v", err)
	}
}

func TestTemplateSendIdempotency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	s := templates.NewStore(pool, 200, 500, 50<<20, 1_000_000)
	id := publishTemplate(t, pool, projectID, v1Source)

	in := templateInput(id, 0, `{"name":"Ada"}`)
	in.IdempotencyKey = "welcome-1"
	first, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	// A newer version needs a variable the retry does not send: the retry
	// still replays the original email instead of failing to render.
	if _, err := s.Publish(ctx, projectID, id, v2Source,
		[]byte(`{"name":{"type":"string","required":true},"plan":{"type":"string","required":true}}`)); err != nil {
		t.Fatal(err)
	}
	reformatted := in
	reformatted.Variables = []byte(` { "name" : "Ada" } `)
	again, err := svc.Create(ctx, projectID, reformatted)
	if err != nil || !again.Replayed || again.ID != first.ID || *again.TemplateVersion != 1 {
		t.Fatalf("replay: %+v %v", again, err)
	}
	// Same key, different variables or a raw request → conflict.
	changed := in
	changed.Variables = []byte(`{"name":"Bob"}`)
	if _, err := svc.Create(ctx, projectID, changed); !errors.Is(err, emails.ErrIdempotencyKeyReused) {
		t.Fatalf("changed variables: %v", err)
	}
	raw := emails.CreateInput{From: in.From, To: in.To, Subject: "Hi", Text: "x", IdempotencyKey: "welcome-1"}
	if _, err := svc.Create(ctx, projectID, raw); !errors.Is(err, emails.ErrIdempotencyKeyReused) {
		t.Fatalf("raw with template key: %v", err)
	}
	// Malformed variables under a key are refused as invalid.
	bad := in
	bad.IdempotencyKey, bad.Variables = "welcome-2", []byte(`{"name":"a","name":"b"}`)
	if _, err := svc.Create(ctx, projectID, bad); !isCode(templates.CodeVariablesInvalid)(err) {
		t.Fatalf("duplicate variable names: %v", err)
	}
	if c := counts(t, pool); c.emails != 1 {
		t.Fatalf("%d emails, want 1", c.emails)
	}
}

func TestTemplateSendCompilesOnlyValidRequestsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	id := publishTemplate(t, pool, projectID, v1Source)
	for _, vars := range []string{`{}`, `{"name":1}`, `{"name":"a","x":1}`, `[]`} {
		if _, err := svc.Create(ctx, projectID, templateInput(id, 0, vars)); !isCode(templates.CodeVariablesInvalid)(err) {
			t.Fatalf("%s: %v", vars, err)
		}
	}
	if n := svc.Compiles(); n != 0 {
		t.Fatalf("invalid variables compiled the template %d times", n)
	}
	for range 3 {
		if _, err := svc.Create(ctx, projectID, templateInput(id, 1, `{"name":"a"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if n := svc.Compiles(); n != 1 {
		t.Fatalf("compiled %d times for three sends of one version, want 1", n)
	}
	// A cached version is never served to another project.
	other, err := auth.CreateProject(ctx, store.New(pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, other.ID, templateInput(id, 1, `{"name":"a"}`)); !isNotFound(err) {
		t.Fatalf("other project with a cached version: %v", err)
	}
}

func TestTemplateRendersAreBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	id := publishTemplate(t, pool, projectID, v1Source)
	svc.SetRenderLimits(1, 50*time.Millisecond)
	release := svc.HoldRenderSlot()
	if _, err := svc.Create(ctx, projectID, templateInput(id, 0, `{"name":"a"}`)); !errors.Is(err, emails.ErrRenderBusy) {
		t.Fatalf("no render slot: %v", err)
	}
	release()
	if _, err := svc.Create(ctx, projectID, templateInput(id, 0, `{"name":"a"}`)); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if c := counts(t, pool); c.emails != 1 {
		t.Fatalf("%d emails, want 1 (busy refusal stores nothing)", c.emails)
	}
}

func TestTemplatePreCheckReplaysConcurrentWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := limitedService(t, pool, 1)
	id := publishTemplate(t, pool, projectID, v1Source)
	in := templateInput(id, 0, `{"name":"a"}`)
	in.IdempotencyKey = "k-1"
	first, err := svc.Create(ctx, projectID, in)
	if err != nil {
		t.Fatal(err)
	}
	// The window is full now; the duplicate must replay, not be refused.
	again, err := svc.Create(ctx, projectID, in)
	if err != nil || !again.Replayed || again.ID != first.ID {
		t.Fatalf("duplicate in a full window: %+v %v", again, err)
	}
}
