//go:build integration

package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/httpapi"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
	"github.com/skraus2/email-platform/internal/testdb"
	"github.com/skraus2/email-platform/internal/webhooks"
)

// tenantTables mirrors the project table list of the store tests (which
// prove it against the schema).
var tenantTables = []string{
	"api_keys", "audience_members", "audiences", "automation_runs", "automations", "broadcasts",
	"contact_event_windows", "contact_events", "contacts", "email_contents", "email_events", "emails",
	"idempotency_keys", "rate_limit_windows", "sender_identities", "suppressions", "template_versions",
	"template_write_windows", "templates", "webhook_deliveries", "webhook_endpoints",
}

// newFullStack is the API with every service wired as cmd/api does,
// including webhook registration.
func newFullStack(t *testing.T) *stack {
	t.Helper()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	logger := slog.New(slog.DiscardHandler)
	enq, err := jobs.NewEnqueuer(pool, logger, 5)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(httpapi.Deps{
		Logger:        logger,
		Emails:        emails.NewService(pool, enq, logger, 1_000_000),
		Templates:     templates.NewStore(pool, 200, 500, 50<<20, 1_000_000),
		Webhooks:      webhooks.Registrar{Pool: pool, Keys: testKeys(t), Policy: webhooks.TargetPolicy{}, MaxEndpoints: 10},
		Contacts:      contacts.NewStore(pool, 1000),
		Audiences:     contacts.NewAudiences(pool, 100, 1000),
		Broadcasts:    broadcasts.NewStore(pool, enq, 20),
		Automations:   automations.NewStore(pool, enq, 50),
		ContactEvents: automations.NewEvents(pool, enq, logger, 1_000_000),
		Runs:          automations.NewRuns(pool),
		Authenticate: func(ctx context.Context, token string) (auth.Principal, error) {
			return auth.Authenticate(ctx, q.GetActiveKeyByPrefix, token)
		},
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return &stack{pool: pool, q: q, srv: srv}
}

type request struct {
	method, path, body string
	header             map[string]string
}

// send performs r with the token and returns status and raw body.
func (s *stack) send(t *testing.T, token string, r request) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), r.method, s.srv.URL+r.path, strings.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// tenant is one project's key and the resources the fixture created for it.
type tenant struct {
	id    uuid.UUID
	token string
	ids   map[string]string
}

func (s *stack) must(t *testing.T, tn *tenant, want int, r request) map[string]any {
	t.Helper()
	code, body := s.doWith(t, r.method, r.path, tn.token, r.body, r.header)
	if code != want {
		t.Fatalf("%s %s: %d %v", r.method, r.path, code, body)
	}
	return body
}

func (s *stack) create(t *testing.T, tn *tenant, name string, want int, r request) string {
	t.Helper()
	id, _ := s.must(t, tn, want, r)["id"].(string)
	if id == "" {
		t.Fatalf("%s: no id", name)
	}
	tn.ids[name] = id
	return id
}

// seed gives the tenant two of everything that lists, so cursors exist.
func (s *stack) seed(t *testing.T, tn *tenant, prefix string) {
	t.Helper()
	ctx := context.Background()
	tmpl := s.create(t, tn, "template", 201, request{method: "POST", path: "/v1/templates", body: `{"slug":"welcome"}`})
	s.must(t, tn, 201, request{method: "POST", path: "/v1/templates/" + tmpl + "/versions", body: jsonBody(t, map[string]any{
		"subject": "Hi", "text": "Hi {{.name}} - {{.unsubscribe_url}}",
		"variables": map[string]any{"unsubscribe_url": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}},
	})})
	for _, n := range []string{"1", "2"} {
		s.create(t, tn, "contact"+n, 201, request{method: "POST", path: "/v1/contacts",
			body: `{"email":"` + prefix + n + `@` + prefix + `.test","status":"subscribed","consent_source":"signup"}`})
		s.create(t, tn, "audience"+n, 201, request{method: "POST", path: "/v1/audiences", body: `{"name":"news` + n + `"}`})
	}
	s.must(t, tn, 200, request{method: "POST", path: "/v1/audiences/" + tn.ids["audience1"] + "/contacts",
		body: `{"contact_ids":["` + tn.ids["contact1"] + `","` + tn.ids["contact2"] + `"]}`})
	for _, n := range []string{"1", "2"} {
		s.create(t, tn, "broadcast"+n, 201, request{method: "POST", path: "/v1/broadcasts", body: jsonBody(t, map[string]any{
			"name": "B" + n, "audience_id": tn.ids["audience1"], "from": "news@example.test", "template_id": tmpl,
			"template_version": 1, "unsubscribe_url": "https://app.example.test/u?t={token}",
		})})
		s.create(t, tn, "automation"+n, 201, request{method: "POST", path: "/v1/automations", body: jsonBody(t, map[string]any{
			"name": "Welcome" + n, "trigger": map[string]any{"event": prefix + ".signed_up" + n},
			"steps": []any{
				map[string]any{"type": "wait", "seconds": 3600},
				map[string]any{"type": "send", "from": "news@example.test", "template_id": tmpl, "template_version": 1,
					"unsubscribe_url": "https://app.example.test/u?t={token}"},
			},
		})})
	}
	for _, n := range []string{"1", "2"} {
		body := s.must(t, tn, 202, request{method: "POST", path: "/v1/contact-events", header: map[string]string{"Idempotency-Key": prefix + "-shared-key" + n},
			body: `{"name":"` + prefix + `.signed_up1","contact_id":"` + tn.ids["contact"+n] + `"}`})
		tn.ids["run"+n] = body["runs"].([]any)[0].(map[string]any)["id"].(string)
	}
	// A plain send under a shared idempotency key, a scheduled send and a
	// template send (which also warms the compiled template cache).
	s.create(t, tn, "email1", 202, request{method: "POST", path: "/v1/emails", header: map[string]string{"Idempotency-Key": prefix + "-shared-key"},
		body: `{"from":"app@example.test","to":"` + prefix + `-recipient@` + prefix + `.test","subject":"Hi","text":"x"}`})
	at := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	s.create(t, tn, "email2", 202, request{method: "POST", path: "/v1/emails",
		body: `{"from":"app@example.test","to":"` + prefix + `-later@` + prefix + `.test","subject":"Later","text":"x","scheduled_at":"` + at + `"}`})
	s.create(t, tn, "email3", 202, request{method: "POST", path: "/v1/emails", body: jsonBody(t, map[string]any{
		"from": "app@example.test", "to": prefix + "-tmpl@" + prefix + ".test", "template_id": tmpl, "template_version": 1,
		"variables": map[string]any{"unsubscribe_url": "https://app.example.test/u", "name": "N"},
	})})
	for i, typ := range []string{"submitted", "captured"} {
		var id string
		if err := s.pool.QueryRow(ctx, `INSERT INTO email_events (project_id, email_id, origin, type, provider, occurred_at, received_at)
			VALUES ($1, $2, 'platform', $3, 'mailpit', now() - make_interval(secs => $4), now()) RETURNING id::text`,
			tn.id, tn.ids["email1"], typ, 10-i).Scan(&id); err != nil {
			t.Fatal(err)
		}
		tn.ids["event"+typ] = id
	}
	s.create(t, tn, "endpoint", 201, request{method: "POST", path: "/v1/webhook-endpoints", body: `{"url":"https://hooks.example.test/` + prefix + `"}`})
	if _, err := s.pool.Exec(ctx, "INSERT INTO suppressions (project_id, recipient, reason) VALUES ($1, $2, 'bounce')", tn.id, "sup@"+prefix+".test"); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := s.pool.QueryRow(ctx, "SELECT unsubscribe_token FROM contacts WHERE id = $1", tn.ids["contact2"]).Scan(&token); err != nil {
		t.Fatal(err)
	}
	tn.ids["unsubscribe_token"] = token
}

// fingerprint hashes the tenant's rows per project table, so any change by
// another project shows, including columns the API never returns.
func (s *stack) fingerprint(t *testing.T, project uuid.UUID) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range tenantTables {
		var sum string
		if err := s.pool.QueryRow(context.Background(),
			`SELECT coalesce(md5(string_agg(x::text, '|' ORDER BY x::text)), '-') FROM `+table+` x WHERE project_id = $1`, project).Scan(&sum); err != nil {
			t.Fatal(err)
		}
		out[table] = sum
	}
	return out
}

type probe struct {
	op, ref string
	// same: the foreign request answers exactly like the unknown one.
	// fresh: reusing another project's natural key or value succeeds as if
	// it were new (want = expected status). list: a plain list or a replayed
	// cursor answers 200/4xx.
	mode    string
	foreign request
	unknown request
	want    int
}

var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// TestEveryRouteIsolatesProjects calls every /v1 operation as project B with
// project A's ids, natural keys, filters and cursors. Each answer equals the
// answer for an unknown value, no answer contains anything of A, A's rows
// stay byte-for-byte unchanged, and nothing of B refers to A.
func TestEveryRouteIsolatesProjects(t *testing.T) {
	t.Parallel()
	s := newFullStack(t)
	ctx := context.Background()
	newTenant := func(prefix string) *tenant {
		id, token := s.project(t, auth.KnownScopes...)
		tn := &tenant{id: id, token: token, ids: map[string]string{}}
		s.seed(t, tn, prefix)
		return tn
	}
	var inSchema []string
	rows, err := s.pool.Query(ctx, `SELECT table_name::text FROM information_schema.columns
		WHERE table_schema = 'public' AND column_name = 'project_id' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		inSchema = append(inSchema, name)
	}
	if err := rows.Err(); err != nil || !slices.Equal(inSchema, tenantTables) {
		t.Fatalf("project tables in the schema %v differ from the list %v (%v)", inSchema, tenantTables, err)
	}
	a, b := newTenant("a"), newTenant("b")
	before := s.fingerprint(t, a.id)

	unknown := func() string { return uuid.Must(uuid.NewV7()).String() }
	cursor := func(path string) string {
		t.Helper()
		body := s.must(t, a, 200, request{method: "GET", path: path})
		c, _ := body["next_cursor"].(string)
		if c == "" {
			t.Fatalf("no cursor for %s: %v", path, body)
		}
		return url.QueryEscape(c)
	}
	same := func(op, ref, method, pathTmpl, bodyTmpl, foreignID string) probe {
		u := unknown()
		r := func(id string) request {
			return request{method: method, path: strings.ReplaceAll(pathTmpl, "{ref}", id), body: strings.ReplaceAll(bodyTmpl, "{ref}", id)}
		}
		return probe{op: op, ref: ref, mode: "same", foreign: r(foreignID), unknown: r(u)}
	}
	get := func(op, ref, path string) probe {
		return probe{op: op, ref: ref, mode: "list", foreign: request{method: "GET", path: path}}
	}
	fresh := func(op, ref string, want int, r request) probe {
		return probe{op: op, ref: ref, mode: "fresh", foreign: r, want: want}
	}
	A := a.ids
	sendBody := `{"from":"app@example.test","to":"x@b.test","template_id":"{ref}","template_version":1,"variables":{"unsubscribe_url":"u","name":"n"}}`
	broadcast := func(audience, tmpl string) string {
		return jsonBody(t, map[string]any{"name": "X", "audience_id": audience, "from": "news@example.test", "template_id": tmpl,
			"template_version": 1, "unsubscribe_url": "https://app.example.test/u?t={token}"})
	}
	automation := jsonBody(t, map[string]any{"name": "X", "trigger": map[string]any{"event": "x"},
		"steps": []any{map[string]any{"type": "send", "from": "news@example.test", "template_id": "{ref}", "template_version": 1,
			"unsubscribe_url": "https://app.example.test/u?t={token}"}}})
	probes := []probe{
		same("POST /v1/emails", "template_id", "POST", "/v1/emails", sendBody, A["template"]),
		fresh("POST /v1/emails", "Idempotency-Key", 202, request{method: "POST", path: "/v1/emails", header: map[string]string{"Idempotency-Key": "a-shared-key"},
			body: `{"from":"app@example.test","to":"a-recipient@a.test","subject":"Hi","text":"x"}`}),
		fresh("POST /v1/emails", "suppressed recipient", 202, request{method: "POST", path: "/v1/emails", body: `{"from":"app@example.test","to":"sup@a.test","subject":"Hi","text":"x"}`}),
		get("GET /v1/emails", "list", "/v1/emails"),
		same("GET /v1/emails", "template_id filter", "GET", "/v1/emails?template_id={ref}", "", A["template"]),
		same("GET /v1/emails", "broadcast_id filter", "GET", "/v1/emails?broadcast_id={ref}", "", A["broadcast1"]),
		{op: "GET /v1/emails", ref: "recipient filter", mode: "same",
			foreign: request{method: "GET", path: "/v1/emails?recipient=a-tmpl@a.test"}, unknown: request{method: "GET", path: "/v1/emails?recipient=nobody@a.test"}},
		get("GET /v1/emails", "cursor", "/v1/emails?limit=1&cursor="+cursor("/v1/emails?limit=1")),
		same("GET /v1/emails/{id}", "id", "GET", "/v1/emails/{ref}", "", A["email1"]),
		same("GET /v1/emails/{id}/events", "id", "GET", "/v1/emails/{ref}/events", "", A["email1"]),
		get("GET /v1/emails/{id}/events", "cursor", "/v1/emails/"+b.ids["email1"]+"/events?limit=1&cursor="+cursor("/v1/emails/"+A["email1"]+"/events?limit=1")),
		same("POST /v1/emails/{id}/cancel", "id", "POST", "/v1/emails/{ref}/cancel", "", A["email2"]),
		fresh("POST /v1/contacts", "email", 201, request{method: "POST", path: "/v1/contacts", body: `{"email":"a1@a.test","status":"unsubscribed"}`}),
		get("GET /v1/contacts", "list", "/v1/contacts"),
		{op: "GET /v1/contacts", ref: "email filter", mode: "same",
			foreign: request{method: "GET", path: "/v1/contacts?email=a2@a.test"}, unknown: request{method: "GET", path: "/v1/contacts?email=nobody@a.test"}},
		get("GET /v1/contacts", "cursor", "/v1/contacts?limit=1&cursor="+cursor("/v1/contacts?limit=1")),
		same("GET /v1/contacts/{id}", "id", "GET", "/v1/contacts/{ref}", "", A["contact1"]),
		same("PATCH /v1/contacts/{id}", "id", "PATCH", "/v1/contacts/{ref}", `{"status":"unsubscribed"}`, A["contact1"]),
		same("DELETE /v1/contacts/{id}", "id", "DELETE", "/v1/contacts/{ref}", "", A["contact1"]),
		{op: "POST /v1/unsubscribes", ref: "token", mode: "same",
			foreign: request{method: "POST", path: "/v1/unsubscribes", body: `{"token":"` + A["unsubscribe_token"] + `"}`},
			unknown: request{method: "POST", path: "/v1/unsubscribes", body: `{"token":"` + strings.Repeat("A", len(A["unsubscribe_token"])) + `"}`}},
		fresh("POST /v1/audiences", "name", 201, request{method: "POST", path: "/v1/audiences", body: `{"name":"news2-a"}`}),
		fresh("POST /v1/audiences", "same name as A", 201, request{method: "POST", path: "/v1/audiences", body: `{"name":"News1 "}`}),
		get("GET /v1/audiences", "list", "/v1/audiences"),
		get("GET /v1/audiences", "cursor", "/v1/audiences?limit=1&cursor="+cursor("/v1/audiences?limit=1")),
		same("GET /v1/audiences/{id}", "id", "GET", "/v1/audiences/{ref}", "", A["audience1"]),
		same("POST /v1/audiences/{id}/contacts", "id", "POST", "/v1/audiences/{ref}/contacts", `{"contact_ids":["`+b.ids["contact1"]+`"]}`, A["audience1"]),
		same("POST /v1/audiences/{id}/contacts", "contact_ids", "POST", "/v1/audiences/"+b.ids["audience2"]+"/contacts", `{"contact_ids":["{ref}"]}`, A["contact1"]),
		same("GET /v1/audiences/{id}/contacts", "id", "GET", "/v1/audiences/{ref}/contacts", "", A["audience1"]),
		get("GET /v1/audiences/{id}/contacts", "cursor", "/v1/audiences/"+b.ids["audience1"]+"/contacts?limit=1&cursor="+cursor("/v1/audiences/"+A["audience1"]+"/contacts?limit=1")),
		same("PUT /v1/audiences/{id}/contacts/{contact_id}", "id", "PUT", "/v1/audiences/{ref}/contacts/"+b.ids["contact1"], "", A["audience2"]),
		same("PUT /v1/audiences/{id}/contacts/{contact_id}", "contact_id", "PUT", "/v1/audiences/"+b.ids["audience2"]+"/contacts/{ref}", "", A["contact1"]),
		same("DELETE /v1/audiences/{id}/contacts/{contact_id}", "id", "DELETE", "/v1/audiences/{ref}/contacts/"+A["contact1"], "", A["audience1"]),
		same("DELETE /v1/audiences/{id}/contacts/{contact_id}", "contact_id", "DELETE", "/v1/audiences/"+b.ids["audience1"]+"/contacts/{ref}", "", A["contact1"]),
		same("POST /v1/broadcasts", "audience_id", "POST", "/v1/broadcasts", broadcast("{ref}", b.ids["template"]), A["audience1"]),
		same("POST /v1/broadcasts", "template_id", "POST", "/v1/broadcasts", broadcast(b.ids["audience1"], "{ref}"), A["template"]),
		get("GET /v1/broadcasts", "list", "/v1/broadcasts"),
		get("GET /v1/broadcasts", "cursor", "/v1/broadcasts?limit=1&cursor="+cursor("/v1/broadcasts?limit=1")),
		same("GET /v1/broadcasts/{id}", "id", "GET", "/v1/broadcasts/{ref}", "", A["broadcast1"]),
		same("POST /v1/broadcasts/{id}/schedule", "id", "POST", "/v1/broadcasts/{ref}/schedule", "", A["broadcast1"]),
		same("POST /v1/broadcasts/{id}/cancel", "id", "POST", "/v1/broadcasts/{ref}/cancel", "", A["broadcast1"]),
		same("POST /v1/automations", "steps[].template_id", "POST", "/v1/automations", automation, A["template"]),
		get("GET /v1/automations", "list", "/v1/automations"),
		get("GET /v1/automations", "cursor", "/v1/automations?limit=1&cursor="+cursor("/v1/automations?limit=1")),
		same("GET /v1/automations/{id}", "id", "GET", "/v1/automations/{ref}", "", A["automation1"]),
		same("PATCH /v1/automations/{id}", "id", "PATCH", "/v1/automations/{ref}", `{"name":"renamed"}`, A["automation1"]),
		same("GET /v1/automations/{id}/runs", "id", "GET", "/v1/automations/{ref}/runs", "", A["automation1"]),
		same("GET /v1/automations/{id}/runs", "contact_id filter", "GET", "/v1/automations/"+b.ids["automation1"]+"/runs?contact_id={ref}", "", A["contact1"]),
		get("GET /v1/automations/{id}/runs", "cursor", "/v1/automations/"+b.ids["automation1"]+"/runs?limit=1&cursor="+cursor("/v1/automations/"+A["automation1"]+"/runs?limit=1")),
		same("GET /v1/automation-runs/{id}", "id", "GET", "/v1/automation-runs/{ref}", "", A["run1"]),
		same("POST /v1/automation-runs/{id}/cancel", "id", "POST", "/v1/automation-runs/{ref}/cancel", "", A["run1"]),
		same("POST /v1/contact-events", "contact_id", "POST", "/v1/contact-events", `{"name":"a.signed_up1","contact_id":"{ref}"}`, A["contact1"]),
		{op: "POST /v1/contact-events", ref: "email", mode: "same",
			foreign: request{method: "POST", path: "/v1/contact-events", body: `{"name":"a.signed_up1","email":"a2@a.test"}`},
			unknown: request{method: "POST", path: "/v1/contact-events", body: `{"name":"a.signed_up1","email":"nobody@a.test"}`}},
		fresh("POST /v1/contact-events", "Idempotency-Key and A's trigger name", 202, request{method: "POST", path: "/v1/contact-events",
			header: map[string]string{"Idempotency-Key": "a-shared-key1"}, body: `{"name":"a.signed_up1","contact_id":"` + b.ids["contact2"] + `"}`}),
		fresh("POST /v1/templates", "slug", 201, request{method: "POST", path: "/v1/templates", body: `{"slug":"welcome-a"}`}),
		same("GET /v1/templates/{id}", "id", "GET", "/v1/templates/{ref}", "", A["template"]),
		same("POST /v1/templates/{id}/versions", "id", "POST", "/v1/templates/{ref}/versions", `{"subject":"S","text":"t"}`, A["template"]),
		same("GET /v1/templates/{id}/versions/{version}", "id", "GET", "/v1/templates/{ref}/versions/1", "", A["template"]),
		fresh("POST /v1/webhook-endpoints", "none: takes no reference", 201, request{method: "POST", path: "/v1/webhook-endpoints", body: `{"url":"https://hooks.example.test/a"}`}),
	}

	// Every contract operation has at least one probe.
	covered := map[string]bool{}
	for _, p := range probes {
		covered[p.op] = true
	}
	for _, op := range contractOperations(t) {
		if !covered[op] {
			t.Errorf("operation %s has no isolation probe", op)
		}
		delete(covered, op)
	}
	for op := range covered {
		t.Errorf("probe for unknown operation %s", op)
	}

	// Everything of A that B must never see.
	var markers []string
	for k, v := range A {
		if k != "unsubscribe_token" {
			markers = append(markers, v)
		}
	}
	markers = append(markers, "a2@a.test", "a-tmpl@a.test", "a-later@a.test", A["unsubscribe_token"])
	normalize := func(body string, ids ...string) string {
		for _, id := range ids {
			body = strings.ReplaceAll(body, id, "<ref>")
		}
		return body
	}
	for _, p := range probes {
		name := p.op + " [" + p.ref + "]"
		code, body := s.send(t, b.token, p.foreign)
		for _, m := range markers {
			if strings.Contains(body, m) {
				t.Errorf("%s: answer contains %q of project A: %s", name, m, body)
			}
		}
		switch p.mode {
		case "same":
			ucode, ubody := s.send(t, b.token, p.unknown)
			if code != ucode || normalize(body, ids(p.foreign, p.unknown)...) != normalize(ubody, ids(p.foreign, p.unknown)...) {
				t.Errorf("%s: foreign %d %s, unknown %d %s", name, code, body, ucode, ubody)
			}
		case "fresh":
			if code != p.want {
				t.Errorf("%s: %d %s, want %d", name, code, body, p.want)
			}
		case "list":
			if code != 200 && (code < 400 || code >= 500) {
				t.Errorf("%s: %d %s", name, code, body)
			}
		default:
			t.Fatalf("%s: unknown mode %q", name, p.mode)
		}
	}

	// A is unchanged, row for row.
	after := s.fingerprint(t, a.id)
	for _, table := range tenantTables {
		if before[table] != after[table] {
			t.Errorf("project A's %s changed by project B's calls", table)
		}
	}
	// Nothing of B refers to A: neither rows (incl. JSON such as automation
	// steps) nor queued jobs.
	var aIDs []string
	for k, v := range A {
		if uuidPattern.MatchString(v) && k != "unsubscribe_token" {
			aIDs = append(aIDs, v)
		}
	}
	pattern := strings.Join(aIDs, "|")
	for _, table := range tenantTables {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` x WHERE project_id = $1 AND x::text ~ $2`, b.id, pattern).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d rows of project B in %s refer to project A", n, table)
		}
	}
	var jobsReferringToA int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'project_id' = $1 AND args::text ~ $2`, b.id.String(), pattern).Scan(&jobsReferringToA); err != nil {
		t.Fatal(err)
	}
	if jobsReferringToA != 0 {
		t.Errorf("%d jobs of project B refer to project A", jobsReferringToA)
	}
	// B's own resources still work: the probes did not break B either.
	if code, body := s.send(t, b.token, request{method: "GET", path: "/v1/contacts/" + b.ids["contact1"]}); code != 200 {
		t.Errorf("B's contact: %d %s", code, body)
	}
}

// ids returns the uuids that differ between the foreign and the unknown
// request: the reference under test.
func ids(foreign, unknown request) []string {
	f := uuidPattern.FindAllString(foreign.path+" "+foreign.body, -1)
	u := uuidPattern.FindAllString(unknown.path+" "+unknown.body, -1)
	var out []string
	for _, id := range f {
		if !slices.Contains(u, id) {
			out = append(out, id)
		}
	}
	for _, id := range u {
		if !slices.Contains(f, id) {
			out = append(out, id)
		}
	}
	return out
}

// TestVerifiedSenderIsPerProject: a sender identity verified by one project
// does not let another project send from it.
func TestVerifiedSenderIsPerProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStackWith(t, 1_000_000, true)
	a, tokenA := s.project(t)
	_, tokenB := s.project(t)
	id, err := emails.AddSender(ctx, s.q, a, emails.SenderDomain, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.VerifySender(ctx, s.q, a, id.ID); err != nil {
		t.Fatal(err)
	}
	if code, body := s.do(t, http.MethodPost, "/v1/emails", tokenA, sendBody); code != http.StatusAccepted {
		t.Fatalf("owner: %d %v", code, body)
	}
	if code, body := s.do(t, http.MethodPost, "/v1/emails", tokenB, sendBody); code != http.StatusUnprocessableEntity || errorCode(body) != "sender_not_verified" {
		t.Fatalf("other project: %d %v, want 422 sender_not_verified", code, body)
	}
}
