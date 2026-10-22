//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/admin"
	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/httpapi"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/templates"
)

const adminPassword = "correct horse battery"

// adminStack is the public API plus the admin API on its own server, on
// the same database, with one admin account "ops".
type adminStack struct {
	*stack
	admin *httptest.Server
	logs  *syncBuffer
}

// syncBuffer is a log sink safe for concurrent handlers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newAdminStack(t *testing.T) *adminStack {
	t.Helper()
	s := newStack(t)
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc := admin.NewService(s.pool, logger, 30*time.Minute, 12*time.Hour)
	if _, err := svc.CreateUser(context.Background(), "ops", adminPassword); err != nil {
		t.Fatal(err)
	}
	a := httpapi.NewAdmin(httpapi.AdminDeps{Logger: logger, Sessions: svc, Views: admin.NewViews(s.pool), ListenHost: "127.0.0.1"})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &adminStack{stack: s, admin: srv, logs: logs}
}

// call sends a request to base with the given Authorization value.
func call(t *testing.T, base, method, path, authorization, body string, hdr ...string) (reply, map[string]any, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, m, string(raw)
}

// reply is the status and headers of a response whose body was read.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (a *adminStack) login(t *testing.T) string {
	t.Helper()
	resp, body, raw := call(t, a.admin.URL, "POST", "/admin/v1/sessions", "", `{"username":"ops","password":"`+adminPassword+`"}`)
	if resp.StatusCode != 201 {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}
	return "Session " + body["token"].(string)
}

var adminRoutes = []struct{ method, path string }{
	{"DELETE", "/admin/v1/sessions/current"}, {"GET", "/admin/v1/me"}, {"GET", "/admin/v1/projects"},
	{"GET", "/admin/v1/emails"}, {"GET", "/admin/v1/emails/" + uuid.NewString()},
	{"GET", "/admin/v1/emails/" + uuid.NewString() + "/events"},
	{"GET", "/admin/v1/emails/" + uuid.NewString() + "/webhook-deliveries"},
	{"GET", "/admin/v1/templates"}, {"GET", "/admin/v1/templates/" + uuid.NewString()},
	{"GET", "/admin/v1/templates/" + uuid.NewString() + "/versions/1"},
	{"POST", "/admin/v1/templates/" + uuid.NewString() + "/versions/1/preview"},
	{"GET", "/admin/v1/senders"}, {"GET", "/admin/v1/webhook-endpoints"},
	{"GET", "/admin/v1/broadcasts"}, {"GET", "/admin/v1/broadcasts/" + uuid.NewString()},
	{"GET", "/admin/v1/automations"}, {"GET", "/admin/v1/automations/" + uuid.NewString()},
	{"GET", "/admin/v1/automations/" + uuid.NewString() + "/runs"}, {"GET", "/admin/v1/automation-runs/" + uuid.NewString()},
}

func TestAdminAPIIsSeparateFromTheProjectAPI(t *testing.T) {
	t.Parallel()
	a := newAdminStack(t)
	_, key := a.project(t)
	session := a.login(t)
	token := strings.TrimPrefix(session, "Session ")

	// Neither listener routes the other's paths.
	if resp, _, _ := call(t, a.srv.URL, "GET", "/admin/v1/me", session, ""); resp.StatusCode != 404 {
		t.Fatalf("admin route on the public listener: %d", resp.StatusCode)
	}
	if resp, _, _ := call(t, a.admin.URL, "GET", "/v1/emails", "Bearer "+key, ""); resp.StatusCode != 404 {
		t.Fatalf("/v1 on the admin listener: %d", resp.StatusCode)
	}
	// A project key never opens an admin route; a session never opens /v1.
	for _, r := range adminRoutes {
		body := ""
		if r.method == "POST" {
			body = `{}`
		}
		for _, authz := range []string{"", "Bearer " + key, "Bearer " + token, "Session ", "Session nope", "Basic b3BzOng="} {
			resp, m, _ := call(t, a.admin.URL, r.method, r.path, authz, body)
			if resp.StatusCode != 401 || errorCode(m) != "unauthenticated" {
				t.Errorf("%s %s with %q: %d %v", r.method, r.path, authz, resp.StatusCode, m)
			}
		}
		// The session in a cookie is not accepted either.
		resp, _, _ := call(t, a.admin.URL, r.method, r.path, "", body, "Cookie", "ep_session="+token)
		if resp.StatusCode != 401 {
			t.Errorf("%s %s with a session cookie: %d", r.method, r.path, resp.StatusCode)
		}
	}
	for _, authz := range []string{session, "Bearer " + token} {
		if resp, _, _ := call(t, a.srv.URL, "GET", "/v1/emails", authz, ""); resp.StatusCode != 401 {
			t.Errorf("/v1 with %q: %d", authz[:7], resp.StatusCode)
		}
	}
}

func TestAdminLoginLifecycle(t *testing.T) {
	t.Parallel()
	a := newAdminStack(t)
	for _, body := range []string{`{"username":"ops","password":"wrong password!!"}`, `{"username":"nobody","password":"` + adminPassword + `"}`} {
		resp, m, raw := call(t, a.admin.URL, "POST", "/admin/v1/sessions", "", body)
		if resp.StatusCode != 401 || errorCode(m) != "invalid_credentials" || strings.Contains(raw, "nobody") {
			t.Fatalf("failed login: %d %s", resp.StatusCode, raw)
		}
	}
	if resp, m, _ := call(t, a.admin.URL, "POST", "/admin/v1/sessions", "", `{"username":"ops","password":"x","extra":1}`); resp.StatusCode != 400 || errorCode(m) != "invalid_json" {
		t.Fatalf("unknown field: %d %v", resp.StatusCode, m)
	}
	session := a.login(t)
	resp, me, _ := call(t, a.admin.URL, "GET", "/admin/v1/me", session, "")
	if resp.StatusCode != 200 || me["username"] != "ops" || me["session_expires_at"] == nil {
		t.Fatalf("me: %d %v", resp.StatusCode, me)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if resp, _, _ := call(t, a.admin.URL, "DELETE", "/admin/v1/sessions/current", session, ""); resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _, _ := call(t, a.admin.URL, "GET", "/admin/v1/me", session, ""); resp.StatusCode != 401 {
		t.Fatalf("me after logout: %d", resp.StatusCode)
	}
	// Lockout surfaces as 429 with Retry-After in whole seconds.
	for range 5 {
		call(t, a.admin.URL, "POST", "/admin/v1/sessions", "", `{"username":"ops","password":"wrong password!!"}`)
	}
	resp, m, _ := call(t, a.admin.URL, "POST", "/admin/v1/sessions", "", `{"username":"ops","password":"`+adminPassword+`"}`)
	if resp.StatusCode != 429 || errorCode(m) != "login_locked" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("locked: %d %v %q", resp.StatusCode, m, resp.Header.Get("Retry-After"))
	}
}

func TestAdminReadEndpoints(t *testing.T) {
	t.Parallel()
	a := newAdminStack(t)
	ctx := context.Background()
	pA, keyA := a.project(t, "emails:send", "emails:read", "templates:read", "templates:write")
	pB, keyB := a.project(t)
	session := a.login(t)
	get := func(path string) map[string]any {
		t.Helper()
		resp, m, raw := call(t, a.admin.URL, "GET", path, session, "")
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, raw)
		}
		return m
	}
	ids := func(m map[string]any) []string {
		var out []string
		for _, item := range m["data"].([]any) {
			out = append(out, item.(map[string]any)["id"].(string))
		}
		return out
	}

	// Data in two projects.
	_, tmpl := a.do(t, "POST", "/v1/templates", keyA, `{"slug":"welcome","name":"Welcome"}`)
	tid := tmpl["id"].(string)
	for _, v := range []string{
		`{"subject":"Old {{.name}}","text":"Old {{.name}}","variables":{"name":{"type":"string","required":true}}}`,
		`{"subject":"New {{.name}}","text":"Hi {{.name}}","html":"<p style=\"color: red\">Hi {{.name}}</p>","variables":{"name":{"type":"string","required":true}}}`,
	} {
		if code, b := a.do(t, "POST", "/v1/templates/"+tid+"/versions", keyA, v); code != 201 {
			t.Fatal(code, b)
		}
	}
	_, e1 := a.do(t, "POST", "/v1/emails", keyA, `{"from":"noreply@example.test","to":"alice@example.test","template_id":"`+tid+`","template_version":1,"variables":{"name":"Ada"}}`)
	_, e2 := a.do(t, "POST", "/v1/emails", keyB, `{"from":"noreply@example.test","to":"bob@example.test","subject":"Raw","text":"t"}`)
	id1, id2 := e1["id"].(string), e2["id"].(string)
	if _, err := a.pool.Exec(ctx, `UPDATE emails SET status = 'failed', error_code = 'provider_rejected', finished_at = now() WHERE id = $1`, id2); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(ctx, `UPDATE emails SET status = 'captured', provider = 'mailpit', provider_id = 'mp-123@example.test', finished_at = now() WHERE id = $1`, id1); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := a.pool.QueryRow(ctx, `INSERT INTO email_events (project_id, email_id, origin, type, provider, occurred_at, received_at)
		VALUES ($1, $2, 'platform', 'captured', 'mailpit', now() - interval '2 seconds', now()) RETURNING id`, pA, id1).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	endpointID := uuid.New()
	if _, err := a.pool.Exec(ctx, `INSERT INTO webhook_endpoints (id, project_id, url, event_types, secret_ciphertext, secret_key_id)
		VALUES ($1, $2, 'https://hooks.example.test/in', ARRAY['email.captured'], $3, 'k1')`, endpointID, pA, make([]byte, 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(ctx, `INSERT INTO webhook_deliveries (project_id, endpoint_id, event_id, status, attempts, last_status_code)
		VALUES ($1, $2, $3, 'pending', 1, 503)`, pA, endpointID, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(ctx, `INSERT INTO sender_identities (project_id, kind, value) VALUES ($1, 'domain', 'example.test')`, pB); err != nil {
		t.Fatal(err)
	}

	// Projects and emails across projects, with filters and search.
	if got := ids(get("/admin/v1/projects")); len(got) != 2 {
		t.Fatalf("projects %v", got)
	}
	if got := ids(get("/admin/v1/emails")); len(got) != 2 || got[0] != id2 {
		t.Fatalf("all emails %v", got)
	}
	for query, want := range map[string]string{
		"project_id=" + pA.String():               id1,
		"status=failed":                           id2,
		"error_code=provider_rejected":            id2,
		"recipient=BOB@example.test":              id2,
		"template_id=" + tid:                      id1,
		"q=" + id1:                                id1,
		"q=alice@example.test":                    id1,
		"q=mp-123@example.test":                   id1,
		"q=" + id2 + "&project_id=" + pA.String(): "",
	} {
		got := ids(get("/admin/v1/emails?" + query))
		if (want == "" && len(got) != 0) || (want != "" && (len(got) != 1 || got[0] != want)) {
			t.Errorf("%s: %v, want %q", query, got, want)
		}
	}
	page1 := get("/admin/v1/emails?limit=1")
	if len(page1["data"].([]any)) != 1 || page1["next_cursor"] == nil {
		t.Fatalf("page 1 %v", page1)
	}
	if resp, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/emails?status=nope", session, ""); resp.StatusCode != 400 || errorCode(m) != "invalid_filter" {
		t.Fatalf("bad status: %d %v", resp.StatusCode, m)
	}

	// Detail, events, deliveries.
	d := get("/admin/v1/emails/" + id1)
	if d["project_name"] == nil || d["provider_id"] != "mp-123@example.test" || d["template_version"] != float64(1) || d["to"] != "alice@example.test" {
		t.Fatalf("detail %v", d)
	}
	if _, ok := d["text"]; ok {
		t.Fatal("detail exposes the body")
	}
	ev := get("/admin/v1/emails/" + id1 + "/events")["data"].([]any)
	if len(ev) != 1 || ev[0].(map[string]any)["occurred_at"] == ev[0].(map[string]any)["received_at"] || ev[0].(map[string]any)["origin"] != "platform" {
		t.Fatalf("events %v", ev)
	}
	dl := get("/admin/v1/emails/" + id1 + "/webhook-deliveries")["data"].([]any)
	if len(dl) != 1 || dl[0].(map[string]any)["endpoint_url"] != "https://hooks.example.test/in" || dl[0].(map[string]any)["last_status_code"] != float64(503) {
		t.Fatalf("deliveries %v", dl)
	}
	for _, p := range []string{"/admin/v1/emails/" + uuid.NewString(), "/admin/v1/emails/nope", "/admin/v1/emails/" + uuid.NewString() + "/events"} {
		if resp, _, _ := call(t, a.admin.URL, "GET", p, session, ""); resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}

	// Templates: list, detail with versions, old version unchanged, preview.
	if got := ids(get("/admin/v1/templates?project_id=" + pB.String())); len(got) != 0 {
		t.Fatalf("templates of B: %v", got)
	}
	td := get("/admin/v1/templates/" + tid)
	if vs := td["versions"].([]any); len(vs) != 2 || vs[0].(map[string]any)["version"] != float64(2) {
		t.Fatalf("template detail %v", td)
	}
	if v1 := get("/admin/v1/templates/" + tid + "/versions/1"); v1["subject"] != "Old {{.name}}" || v1["html"] != nil {
		t.Fatalf("version 1 %v", v1)
	}
	resp, pv, raw := call(t, a.admin.URL, "POST", "/admin/v1/templates/"+tid+"/versions/2/preview", session, `{"variables":{"name":"<b>x</b>"}}`)
	if resp.StatusCode != 200 || pv["html"] != `<p style="color: red">Hi &lt;b&gt;x&lt;/b&gt;</p>` || pv["subject"] != "New <b>x</b>" {
		t.Fatalf("preview: %d %s", resp.StatusCode, raw)
	}
	if resp, m, _ := call(t, a.admin.URL, "POST", "/admin/v1/templates/"+tid+"/versions/2/preview", session, `{"variables":{}}`); resp.StatusCode != 422 || errorCode(m) != "variables_invalid" {
		t.Fatalf("preview without variables: %d %v", resp.StatusCode, m)
	}
	var stored int
	if err := a.pool.QueryRow(ctx, "SELECT count(*) FROM emails").Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("preview stored something: %d %v", stored, err)
	}

	// Senders and webhook endpoints, never secrets.
	if s := get("/admin/v1/senders")["data"].([]any); len(s) != 1 || s[0].(map[string]any)["value"] != "example.test" {
		t.Fatalf("senders %v", s)
	}
	_, _, rawEndpoints := call(t, a.admin.URL, "GET", "/admin/v1/webhook-endpoints?project_id="+pA.String(), session, "")
	if !strings.Contains(rawEndpoints, endpointID.String()) || strings.Contains(rawEndpoints, "secret") || strings.Contains(rawEndpoints, "cipher") {
		t.Fatalf("endpoints %s", rawEndpoints)
	}
}

// keysOf returns the sorted keys of a JSON object.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func assertKeys(t *testing.T, what string, m map[string]any, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := keysOf(m); !slices.Equal(got, want) {
		t.Errorf("%s keys %v, want %v", what, got, want)
	}
}

func TestAdminContractAndHardening(t *testing.T) {
	t.Parallel()
	a := newAdminStack(t)
	ctx := context.Background()
	pA, keyA := a.project(t, "emails:send", "emails:read", "templates:read", "templates:write")
	pB, keyB := a.project(t, "emails:send", "emails:read", "templates:read", "templates:write")
	session := a.login(t)
	token := strings.TrimPrefix(session, "Session ")

	// Data: 5 emails over two projects, templates and endpoints in both.
	var all []string
	for i, key := range []string{keyA, keyB, keyA, keyB, keyA} {
		_, e := a.do(t, "POST", "/v1/emails", key, `{"from":"noreply@example.test","to":"user`+string(rune('a'+i))+`@example.test","subject":"s","text":"t"}`)
		all = append([]string{e["id"].(string)}, all...)
	}
	var tids []string
	for _, key := range []string{keyA, keyB} {
		_, tm := a.do(t, "POST", "/v1/templates", key, `{"slug":"t"}`)
		tid := tm["id"].(string)
		tids = append(tids, tid)
		a.do(t, "POST", "/v1/templates/"+tid+"/versions", key, `{"subject":"S {{.v}}","text":"T {{.v}}","html":"<p>{{.v}}</p>","variables":{"v":{"type":"string"}}}`)
	}
	for _, p := range []uuid.UUID{pA, pB} {
		if _, err := a.pool.Exec(ctx, `INSERT INTO webhook_endpoints (id, project_id, url, event_types, secret_ciphertext, secret_key_id)
			VALUES ($1, $2, 'https://hooks.example.test/x', ARRAY['email.failed'], $3, 'k1')`, uuid.New(), p, make([]byte, 40)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.pool.Exec(ctx, `INSERT INTO sender_identities (project_id, kind, value) VALUES ($1, 'address', 'a@example.test')`, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.pool.Exec(ctx, `UPDATE emails SET provider = 'mailpit', provider_id = '<msg-1@example.test>', status = 'captured', finished_at = now() WHERE id = $1`, all[0]); err != nil {
		t.Fatal(err)
	}

	// Paging to the end returns every email exactly once, newest first.
	var walked []string
	cursor := ""
	for range 10 {
		path := "/admin/v1/emails?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		_, m, _ := call(t, a.admin.URL, "GET", path, session, "")
		for _, item := range m["data"].([]any) {
			walked = append(walked, item.(map[string]any)["id"].(string))
		}
		next, _ := m["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if !slices.Equal(walked, all) {
		t.Fatalf("paged %v, want %v", walked, all)
	}
	// Unfiltered lists cover every project.
	for path, want := range map[string]int{"/admin/v1/templates": 2, "/admin/v1/webhook-endpoints": 2, "/admin/v1/senders": 2, "/admin/v1/projects": 2} {
		_, m, _ := call(t, a.admin.URL, "GET", path, session, "")
		if n := len(m["data"].([]any)); n != want {
			t.Errorf("%s: %d rows, want %d", path, n, want)
		}
	}
	// A bare Message-ID finds the stored bracketed one.
	if _, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/emails?q=msg-1@example.test", session, ""); len(m["data"].([]any)) != 1 {
		t.Fatalf("bare message id: %v", m)
	}

	// Response shapes match api/admin-openapi.yaml exactly.
	_, me, _ := call(t, a.admin.URL, "GET", "/admin/v1/me", session, "")
	assertKeys(t, "Me", me, "id", "username", "session_expires_at")
	_, em, _ := call(t, a.admin.URL, "GET", "/admin/v1/emails/"+all[0], session, "")
	assertKeys(t, "AdminEmail", em, "id", "project_id", "project_name", "status", "from", "to", "subject", "provider",
		"provider_id", "attempts", "error_code", "template_id", "template_version", "created_at", "updated_at", "finished_at",
		"scheduled_at", "broadcast_id", "automation_id", "automation_run_id")
	_, pr, _ := call(t, a.admin.URL, "GET", "/admin/v1/projects", session, "")
	assertKeys(t, "Project", pr["data"].([]any)[0].(map[string]any), "id", "name", "status", "created_at")
	_, tl, _ := call(t, a.admin.URL, "GET", "/admin/v1/templates", session, "")
	assertKeys(t, "Template", tl["data"].([]any)[0].(map[string]any), "id", "project_id", "slug", "name", "latest_version", "created_at")
	_, td, _ := call(t, a.admin.URL, "GET", "/admin/v1/templates/"+tids[0], session, "")
	assertKeys(t, "TemplateDetail", td, "id", "project_id", "slug", "name", "latest_version", "created_at", "versions")
	_, tv, _ := call(t, a.admin.URL, "GET", "/admin/v1/templates/"+tids[0]+"/versions/1", session, "")
	assertKeys(t, "TemplateVersion", tv, "template_id", "project_id", "version", "subject", "text", "html", "variables", "created_at")
	_, rd, _ := call(t, a.admin.URL, "POST", "/admin/v1/templates/"+tids[0]+"/versions/1/preview", session, `{"variables":{"v":"Zq9-preview-secret"}}`)
	assertKeys(t, "Rendered", rd, "subject", "text", "html")
	_, sd, _ := call(t, a.admin.URL, "GET", "/admin/v1/senders", session, "")
	assertKeys(t, "Sender", sd["data"].([]any)[0].(map[string]any), "id", "project_id", "kind", "value", "status", "verified_at", "created_at")
	_, we, _ := call(t, a.admin.URL, "GET", "/admin/v1/webhook-endpoints", session, "")
	assertKeys(t, "WebhookEndpoint", we["data"].([]any)[0].(map[string]any), "id", "project_id", "url", "event_types", "status", "created_at", "updated_at")
	assertKeys(t, "list", we, "data", "next_cursor")

	// Malformed queries are 400, unknown ids 404, oversized bodies 413.
	for _, q := range []struct{ path, code string }{
		{"/admin/v1/emails?limit=0", "invalid_limit"}, {"/admin/v1/emails?limit=101", "invalid_limit"},
		{"/admin/v1/emails?cursor=nope!", "invalid_cursor"}, {"/admin/v1/emails?nope=1", "invalid_filter"},
		{"/admin/v1/emails?recipient=a@example.test&recipient=b@example.test", "invalid_filter"},
		{"/admin/v1/emails?created_after=2026-10-02T00:00:00Z&created_before=2026-10-01T00:00:00Z", "invalid_filter"},
		{"/admin/v1/emails?project_id=nope", "invalid_filter"}, {"/admin/v1/emails?error_code=Bad-Code", "invalid_filter"},
		{"/admin/v1/projects?limit=abc", "invalid_limit"}, {"/admin/v1/templates?project_id=1", "invalid_filter"},
		{"/admin/v1/emails?q=" + strings.Repeat("a", 513), "invalid_filter"},
	} {
		resp, m, _ := call(t, a.admin.URL, "GET", q.path, session, "")
		if resp.StatusCode != 400 || errorCode(m) != q.code {
			t.Errorf("%s: %d %v, want 400 %s", q.path[:min(len(q.path), 60)], resp.StatusCode, m, q.code)
		}
	}
	for _, p := range []struct{ method, path string }{
		{"GET", "/admin/v1/templates/" + uuid.NewString()},
		{"GET", "/admin/v1/templates/" + tids[0] + "/versions/2"},
		{"GET", "/admin/v1/templates/" + tids[0] + "/versions/0"},
		{"GET", "/admin/v1/emails/" + uuid.NewString() + "/webhook-deliveries"},
		{"POST", "/admin/v1/templates/" + uuid.NewString() + "/versions/1/preview"},
		{"POST", "/admin/v1/templates/" + tids[0] + "/versions/9/preview"},
	} {
		body := ""
		if p.method == "POST" {
			body = `{"variables":{}}`
		}
		resp, _, _ := call(t, a.admin.URL, p.method, p.path, session, body)
		if resp.StatusCode != 404 {
			t.Errorf("%s %s: %d, want 404", p.method, p.path, resp.StatusCode)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("404 without no-store")
		}
	}
	big := `{"variables":{"v":"` + strings.Repeat("a", 1<<20) + `"}}`
	for _, p := range []string{"/admin/v1/templates/" + tids[0] + "/versions/1/preview", "/admin/v1/sessions"} {
		authz := session
		if strings.HasSuffix(p, "sessions") {
			authz = ""
		}
		if resp, m, _ := call(t, a.admin.URL, "POST", p, authz, big); resp.StatusCode != 413 || errorCode(m) != "payload_too_large" {
			t.Errorf("%s oversized: %d %v", p, resp.StatusCode, m)
		}
	}

	// Foreign Host headers are refused (DNS rebinding); loopback names pass.
	if resp, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/me", session, "", "Host", "evil.example.test"); resp.StatusCode != 421 || errorCode(m) != "misdirected_request" {
		t.Fatalf("foreign host: %d %v", resp.StatusCode, m)
	}
	if resp, _, _ := call(t, a.admin.URL, "GET", "/admin/v1/me", session, "", "Host", "localhost:8081"); resp.StatusCode != 200 {
		t.Fatalf("localhost host: %d", resp.StatusCode)
	}

	// no-store on every answer, also 204 and 401.
	if resp, _, _ := call(t, a.admin.URL, "GET", "/admin/v1/me", "", ""); resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("401 without no-store")
	}
	if resp, _, _ := call(t, a.admin.URL, "DELETE", "/admin/v1/sessions/current", session, ""); resp.StatusCode != 204 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("logout: %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}

	// Nothing secret reached the logs.
	logs := a.logs.String()
	for _, secret := range []string{adminPassword, token, "Zq9-preview-secret", keyA, keyB} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain a secret (%.6s…)", secret)
		}
	}
}

func TestAdminBroadcasts(t *testing.T) {
	t.Parallel()
	a := newAdminStack(t)
	session := a.login(t)
	projectA, token := a.project(t, "broadcasts:read", "broadcasts:write", "contacts:write", "templates:write", "emails:send")
	_, tokenB := a.project(t, "broadcasts:write", "contacts:write", "templates:write")
	mk := func(token string) string {
		t.Helper()
		_, body := a.do(t, "POST", "/v1/audiences", token, `{"name":"news"}`)
		audience := body["id"].(string)
		_, body = a.do(t, "POST", "/v1/templates", token, `{"slug":"news"}`)
		tmpl := body["id"].(string)
		a.do(t, "POST", "/v1/templates/"+tmpl+"/versions", token, jsonBody(t, map[string]any{
			"subject": "News", "text": "{{.unsubscribe_url}}",
			"variables": map[string]any{"unsubscribe_url": map[string]any{"type": "string"}},
		}))
		code, body := a.do(t, "POST", "/v1/broadcasts", token, jsonBody(t, map[string]any{
			"name": "October", "audience_id": audience, "from": "news@example.test", "template_id": tmpl,
			"template_version": 1, "variables": map[string]any{}, "unsubscribe_url": "https://app.example.test/u?t={token}"}))
		if code != 201 {
			t.Fatalf("create: %d %v", code, body)
		}
		return body["id"].(string)
	}
	idA, idB := mk(token), mk(tokenB)
	a.do(t, "POST", "/v1/broadcasts/"+idB+"/cancel", tokenB, "")

	// A recipient email of broadcast A, and a planned, cancelled single send.
	var email string
	if err := a.pool.QueryRow(context.Background(), `INSERT INTO emails (project_id, sender, recipient, subject, broadcast_id)
		VALUES ($1, 'news@example.test', 'r@example.test', 'News', $2) RETURNING id`, projectA, idA).Scan(&email); err != nil {
		t.Fatal(err)
	}
	_, body := a.do(t, "POST", "/v1/emails", token, `{"from":"app@example.test","to":"u@example.test","subject":"Later","text":"x","scheduled_at":"`+
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339)+`"}`)
	planned := body["id"].(string)
	a.do(t, "POST", "/v1/emails/"+planned+"/cancel", token, "")

	_, list, _ := call(t, a.admin.URL, "GET", "/admin/v1/broadcasts", session, "")
	if n := len(list["data"].([]any)); n != 2 {
		t.Fatalf("all projects: %d", n)
	}
	first := list["data"].([]any)[0].(map[string]any)
	assertKeys(t, "Broadcast", first, "id", "project_id", "project_name", "name", "status", "audience_id", "from", "template_id",
		"template_version", "unsubscribe_url", "scheduled_at", "started_at", "finished_at", "created_at")
	if _, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/broadcasts?status=canceled", session, ""); len(m["data"].([]any)) != 1 {
		t.Fatalf("status filter: %v", m)
	}
	if _, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/broadcasts?project_id="+projectA.String(), session, ""); len(m["data"].([]any)) != 1 {
		t.Fatalf("project filter: %v", m)
	}
	resp, detail, raw := call(t, a.admin.URL, "GET", "/admin/v1/broadcasts/"+idA, session, "")
	counts, _ := detail["counts"].(map[string]any)
	byStatus, _ := counts["by_status"].(map[string]any)
	if resp.StatusCode != 200 || byStatus["queued"] != 1.0 || len(byStatus) != 9 || counts["recipients"] != 0.0 {
		t.Fatalf("detail: %d %s", resp.StatusCode, raw)
	}
	assertKeys(t, "BroadcastDetail", detail, "id", "project_id", "project_name", "name", "status", "audience_id", "from", "template_id",
		"template_version", "unsubscribe_url", "scheduled_at", "started_at", "finished_at", "created_at", "counts")
	assertKeys(t, "BroadcastDetail.counts", counts, "recipients", "skipped_unsubscribed", "skipped_suppressed", "skipped_invalid", "by_status")
	for _, q := range []string{"/admin/v1/broadcasts?status=sent", "/admin/v1/broadcasts?project_id=x", "/admin/v1/emails?broadcast_id=x"} {
		if resp, m, _ := call(t, a.admin.URL, "GET", q, session, ""); resp.StatusCode != 400 || errorCode(m) != "invalid_filter" {
			t.Errorf("%s: %d %v", q, resp.StatusCode, m)
		}
	}
	if resp, _, _ := call(t, a.admin.URL, "GET", "/admin/v1/broadcasts/"+uuid.NewString(), session, ""); resp.StatusCode != 404 {
		t.Fatalf("unknown: %d", resp.StatusCode)
	}
	if resp, _, _ := call(t, a.admin.URL, "GET", "/admin/v1/broadcasts", "", ""); resp.StatusCode != 401 {
		t.Fatalf("no session: %d", resp.StatusCode)
	}

	// Emails: broadcast filter, broadcast link, planned time, canceled status.
	_, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/emails?broadcast_id="+idA, session, "")
	if data := m["data"].([]any); len(data) != 1 || data[0].(map[string]any)["id"] != email || data[0].(map[string]any)["broadcast_id"] != idA {
		t.Fatalf("broadcast filter: %v", m)
	}
	_, m, _ = call(t, a.admin.URL, "GET", "/admin/v1/emails?status=canceled", session, "")
	if data := m["data"].([]any); len(data) != 1 || data[0].(map[string]any)["id"] != planned || data[0].(map[string]any)["scheduled_at"] == nil {
		t.Fatalf("canceled filter: %v", m)
	}
	all := "/admin/v1/emails?status=queued&status=sending&status=captured&status=submitted&status=delivered&status=bounced&status=complained&status=failed&status=canceled"
	if resp, _, _ := call(t, a.admin.URL, "GET", all, session, ""); resp.StatusCode != 200 {
		t.Fatalf("every status: %d", resp.StatusCode)
	}
	if resp, m, _ := call(t, a.admin.URL, "GET", all+"&status=queued", session, ""); resp.StatusCode != 400 || errorCode(m) != "invalid_filter" {
		t.Fatalf("10 statuses: %d %v", resp.StatusCode, m)
	}
}

func TestAdminAutomations(t *testing.T) {
	t.Parallel()
	a := newAdminStack(t)
	session := a.login(t)
	projectA, token := a.project(t, "automations:write", "events:write", "contacts:write", "templates:write")
	_, tokenB := a.project(t, "automations:write", "templates:write")
	mk := func(token string) string {
		t.Helper()
		_, body := a.do(t, "POST", "/v1/templates", token, `{"slug":"welcome"}`)
		tmpl := body["id"].(string)
		a.do(t, "POST", "/v1/templates/"+tmpl+"/versions", token, jsonBody(t, map[string]any{
			"subject": "Welcome", "text": "{{.unsubscribe_url}}",
			"variables": map[string]any{"unsubscribe_url": map[string]any{"type": "string"}, "plan": map[string]any{"type": "string"}},
		}))
		code, body := a.do(t, "POST", "/v1/automations", token, jsonBody(t, map[string]any{
			"name": "Welcome", "trigger": map[string]any{"event": "user.signed_up"},
			"steps": []any{
				map[string]any{"type": "send", "from": "news@example.test", "template_id": tmpl, "template_version": 1,
					"variables": map[string]any{"plan": "Zq9-shared-value"}, "unsubscribe_url": "https://app.example.test/u?t={token}"},
				map[string]any{"type": "wait", "seconds": 3600},
				map[string]any{"type": "condition", "attribute": "plan", "op": "eq", "value": "pro"},
			},
		}))
		if code != 201 {
			t.Fatalf("create: %d %v", code, body)
		}
		return body["id"].(string)
	}
	idA, idB := mk(token), mk(tokenB)
	a.do(t, "PATCH", "/v1/automations/"+idB, tokenB, `{"status":"paused"}`)
	_, body := a.do(t, "POST", "/v1/contacts", token, `{"email":"ann@example.test","status":"subscribed","consent_source":"signup"}`)
	_, body = a.do(t, "POST", "/v1/contact-events", token, `{"name":"user.signed_up","contact_id":"`+body["id"].(string)+`","data":{"secret":"Zq9-event-data"}}`)
	run := body["runs"].([]any)[0].(map[string]any)["id"].(string)
	enq, err := jobs.NewEnqueuer(a.pool, slog.New(slog.DiscardHandler), 5)
	if err != nil {
		t.Fatal(err)
	}
	engine := automations.NewEngine(a.pool, enq, slog.New(slog.DiscardHandler), templates.NewCache(1<<20, 1<<20))
	if err := engine.Step(context.Background(), jobs.AutomationStepArgs{RunID: uuid.MustParse(run), ProjectID: projectA, Step: 0}, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatal(err)
	}

	_, list, _ := call(t, a.admin.URL, "GET", "/admin/v1/automations", session, "")
	if n := len(list["data"].([]any)); n != 2 {
		t.Fatalf("all projects: %d", n)
	}
	assertKeys(t, "Automation", list["data"].([]any)[0].(map[string]any), "id", "project_id", "project_name", "name", "status",
		"trigger_event", "allow_reentry", "step_count", "created_at", "updated_at")
	if _, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/automations?status=paused", session, ""); len(m["data"].([]any)) != 1 {
		t.Fatalf("status filter: %v", m)
	}
	if _, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/automations?project_id="+projectA.String(), session, ""); len(m["data"].([]any)) != 1 {
		t.Fatalf("project filter: %v", m)
	}
	resp, detail, raw := call(t, a.admin.URL, "GET", "/admin/v1/automations/"+idA, session, "")
	if resp.StatusCode != 200 || detail["run_counts"].(map[string]any)["active"] != 1.0 || len(detail["steps"].([]any)) != 3 {
		t.Fatalf("detail: %d %s", resp.StatusCode, raw)
	}
	assertKeys(t, "AutomationDetail", detail, "id", "project_id", "project_name", "name", "status", "trigger_event", "allow_reentry",
		"step_count", "created_at", "updated_at", "steps", "run_counts")
	assertKeys(t, "SendStep", detail["steps"].([]any)[0].(map[string]any), "type", "from", "template_id", "template_version", "unsubscribe_url")
	assertKeys(t, "ConditionStep", detail["steps"].([]any)[2].(map[string]any), "type", "attribute", "op", "value")
	if strings.Contains(raw, "Zq9-shared-value") {
		t.Fatal("shared variables shown to operators")
	}
	_, runs, raw := call(t, a.admin.URL, "GET", "/admin/v1/automations/"+idA+"/runs?status=active", session, "")
	if len(runs["data"].([]any)) != 1 {
		t.Fatalf("runs: %s", raw)
	}
	assertKeys(t, "AutomationRun", runs["data"].([]any)[0].(map[string]any), "id", "contact_id", "status", "reason", "step",
		"next_step_at", "created_at", "updated_at", "finished_at")
	if _, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/automations/"+idA+"/runs?status=completed", session, ""); len(m["data"].([]any)) != 0 {
		t.Fatalf("run status filter: %v", m)
	}
	// A second run; pages of one run each, newest first.
	_, body = a.do(t, "POST", "/v1/contacts", token, `{"email":"bob@example.test","status":"subscribed","consent_source":"signup"}`)
	_, body = a.do(t, "POST", "/v1/contact-events", token, `{"name":"user.signed_up","contact_id":"`+body["id"].(string)+`"}`)
	second := body["runs"].([]any)[0].(map[string]any)["id"].(string)
	var walked []string
	for cursor := ""; ; {
		_, m, raw := call(t, a.admin.URL, "GET", "/admin/v1/automations/"+idA+"/runs?limit=1"+cursor, session, "")
		data, _ := m["data"].([]any)
		if len(data) != 1 {
			t.Fatalf("run page: %s", raw)
		}
		walked = append(walked, data[0].(map[string]any)["id"].(string))
		next, _ := m["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = "&cursor=" + next
	}
	if len(walked) != 2 || walked[0] != second || walked[1] != run {
		t.Fatalf("runs walked %v, want [%s %s]", walked, second, run)
	}
	_, m1, _ := call(t, a.admin.URL, "GET", "/admin/v1/automations?limit=1", session, "")
	_, m2, _ := call(t, a.admin.URL, "GET", "/admin/v1/automations?limit=1&cursor="+m1["next_cursor"].(string), session, "")
	if m1["data"].([]any)[0].(map[string]any)["id"] != idB || m2["data"].([]any)[0].(map[string]any)["id"] != idA || m2["next_cursor"] != nil {
		t.Fatalf("automation pages: %v %v", m1, m2)
	}
	resp, runDetail, raw := call(t, a.admin.URL, "GET", "/admin/v1/automation-runs/"+run, session, "")
	emailsOfRun, _ := runDetail["emails"].([]any)
	if resp.StatusCode != 200 || len(emailsOfRun) != 1 || runDetail["automation_name"] != "Welcome" || runDetail["step"] != 2.0 {
		t.Fatalf("run detail: %d %s", resp.StatusCode, raw)
	}
	assertKeys(t, "AutomationRunDetail", runDetail, "id", "contact_id", "status", "reason", "step", "next_step_at", "created_at",
		"updated_at", "finished_at", "project_id", "project_name", "automation_id", "automation_name", "emails")
	assertKeys(t, "AutomationRunDetail.emails", emailsOfRun[0].(map[string]any), "id", "step", "status")
	if strings.Contains(raw, "Zq9-event-data") || strings.Contains(raw, "secret") {
		t.Fatal("trigger data shown to operators")
	}
	email := emailsOfRun[0].(map[string]any)["id"].(string)
	if _, m, _ := call(t, a.admin.URL, "GET", "/admin/v1/emails/"+email, session, ""); m["automation_run_id"] != run || m["automation_id"] != idA {
		t.Fatalf("email links: %v", m)
	}
	for _, q := range []string{"/admin/v1/automations?status=done", "/admin/v1/automations?project_id=x", "/admin/v1/automations/" + idA + "/runs?status=x"} {
		if resp, m, _ := call(t, a.admin.URL, "GET", q, session, ""); resp.StatusCode != 400 || errorCode(m) != "invalid_filter" {
			t.Errorf("%s: %d %v", q, resp.StatusCode, m)
		}
	}
	for _, p := range []string{"/admin/v1/automations/" + uuid.NewString(), "/admin/v1/automations/" + uuid.NewString() + "/runs", "/admin/v1/automation-runs/" + uuid.NewString()} {
		if resp, _, _ := call(t, a.admin.URL, "GET", p, session, ""); resp.StatusCode != 404 {
			t.Errorf("unknown %s: %d", p, resp.StatusCode)
		}
	}
}
