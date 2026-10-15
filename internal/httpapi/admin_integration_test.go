//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/admin"
	"github.com/skraus2/email-platform/internal/httpapi"
)

const adminPassword = "correct horse battery"

// adminStack is the public API plus the admin API on its own server, on
// the same database, with one admin account "ops".
type adminStack struct {
	*stack
	admin *httptest.Server
}

func newAdminStack(t *testing.T) *adminStack {
	t.Helper()
	s := newStack(t)
	logger := slog.New(slog.DiscardHandler)
	svc := admin.NewService(s.pool, logger, 30*time.Minute, 12*time.Hour)
	if _, err := svc.CreateUser(context.Background(), "ops", adminPassword); err != nil {
		t.Fatal(err)
	}
	a := httpapi.NewAdmin(httpapi.AdminDeps{Logger: logger, Sessions: svc, Views: admin.NewViews(s.pool)})
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return &adminStack{stack: s, admin: srv}
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
