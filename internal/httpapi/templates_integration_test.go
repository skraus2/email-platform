//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (s *stack) raw(t *testing.T, method, path, token, contentType, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, s.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTemplateLifecycleOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "templates:read", "templates:write")

	code, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"welcome","name":"Welcome"}`)
	if code != 201 || body["slug"] != "welcome" || body["name"] != "Welcome" || body["latest_version"] != nil {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	if code, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"welcome"}`); code != 409 || errorCode(body) != "slug_taken" {
		t.Fatalf("duplicate: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"Bad Slug"}`); code != 422 || errorCode(body) != "validation_failed" {
		t.Fatalf("bad slug: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"x","extra":1}`); code != 400 || errorCode(body) != "invalid_json" {
		t.Fatalf("unknown field: %d %v", code, body)
	}
	if code, _ := s.raw(t, "POST", "/v1/templates", token, "text/plain", `{"slug":"y"}`); code != 415 {
		t.Fatalf("content type: %d", code)
	}

	v1 := map[string]any{
		"subject":   "  Hi {{ .name }}\t",
		"text":      "Hello {{.name}}\r\n\n  ünïcode ✓ \"quoted\"",
		"html":      "<p>\n  {{.name}}\n</p>",
		"variables": json.RawMessage(`{ "name" : { "required":true, "type":"string" } }`),
	}
	code, body = s.do(t, "POST", "/v1/templates/"+id+"/versions", token, jsonBody(t, v1))
	if code != 201 || body["version"] != float64(1) || body["template_id"] != id {
		t.Fatalf("publish v1: %d %v", code, body)
	}
	if code, body = s.do(t, "POST", "/v1/templates/"+id+"/versions", token, `{"subject":"v2","text":"v2"}`); code != 201 || body["version"] != float64(2) {
		t.Fatalf("publish v2: %d %v", code, body)
	}
	if code, body = s.do(t, "GET", "/v1/templates/"+id, token, ""); code != 200 || body["latest_version"] != float64(2) {
		t.Fatalf("get: %d %v", code, body)
	}

	// The old version is returned exactly as published.
	code, raw := s.raw(t, "GET", "/v1/templates/"+id+"/versions/1", token, "", "")
	if code != 200 {
		t.Fatalf("get v1: %d %s", code, raw)
	}
	var got struct {
		Subject, Text string
		HTML          *string
		Variables     json.RawMessage
		Version       int
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Subject != v1["subject"] || got.Text != v1["text"] || got.HTML == nil || *got.HTML != v1["html"] || got.Version != 1 {
		t.Fatalf("v1 changed: %+v", got)
	}
	if string(got.Variables) != `{"name":{"type":"string","required":true,"max_length":1000}}` {
		t.Fatalf("variables %s", got.Variables)
	}
	code, body = s.do(t, "GET", "/v1/templates/"+id+"/versions/2", token, "")
	if code != 200 || body["html"] != nil || body["variables"] == nil {
		t.Fatalf("get v2: %d %v", code, body)
	}

	// Validation errors name the part and line, never accept bad sources.
	code, body = s.do(t, "POST", "/v1/templates/"+id+"/versions", token, `{"subject":"x","text":"a\n{{printf \"%s\" .name}}"}`)
	if code != 422 || errorCode(body) != "template_invalid" || !strings.Contains(body["error"].(map[string]any)["message"].(string), "text: line 2") {
		t.Fatalf("invalid template: %d %v", code, body)
	}
	if code, body = s.do(t, "POST", "/v1/templates/"+id+"/versions", token, `{"subject":"x","html":"<p>x</p>"}`); code != 422 || errorCode(body) != "template_invalid" {
		t.Fatalf("missing text: %d %v", code, body)
	}
	if code, body = s.do(t, "POST", "/v1/templates/"+id+"/versions", token, `{"subject":"x","text":"t","variables":{"n":{"type":"date"}}}`); code != 422 || errorCode(body) != "validation_failed" {
		t.Fatalf("invalid schema: %d %v", code, body)
	}
	if code, body = s.do(t, "GET", "/v1/templates/"+id+"/versions/3", token, ""); code != 404 {
		t.Fatalf("unknown version: %d %v", code, body)
	}
}

func TestTemplateIsolationAndScopes(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, owner := s.project(t, "templates:read", "templates:write")
	_, other := s.project(t, "templates:read", "templates:write")
	_, readOnly := s.project(t, "templates:read")
	_, emailsOnly := s.project(t)

	code, body := s.do(t, "POST", "/v1/templates", owner, `{"slug":"welcome"}`)
	if code != 201 {
		t.Fatal(code, body)
	}
	id := body["id"].(string)
	if code, _ := s.do(t, "POST", "/v1/templates/"+id+"/versions", owner, `{"subject":"s","text":"t"}`); code != 201 {
		t.Fatal(code)
	}

	// Another project sees the same 404 as for an id that does not exist.
	_, unknown := s.raw(t, "GET", "/v1/templates/"+uuid.NewString(), other, "", "")
	for _, p := range []struct{ method, path, body string }{
		{"GET", "/v1/templates/" + id, ""},
		{"GET", "/v1/templates/" + id + "/versions/1", ""},
		{"POST", "/v1/templates/" + id + "/versions", `{"subject":"s","text":"t"}`},
	} {
		ct := ""
		if p.body != "" {
			ct = "application/json"
		}
		code, raw := s.raw(t, p.method, p.path, other, ct, p.body)
		if code != 404 || raw != unknown {
			t.Errorf("foreign %s %s: %d %s, want the unknown-id 404 %s", p.method, p.path, code, raw, unknown)
		}
	}
	var n int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM template_versions").Scan(&n); err != nil || n != 1 {
		t.Fatalf("foreign publish stored a version (%d, %v)", n, err)
	}

	// Scopes: read-only keys cannot write; email keys see nothing.
	if code, _ := s.do(t, "POST", "/v1/templates", readOnly, `{"slug":"x"}`); code != 403 {
		t.Fatalf("read-only create: %d", code)
	}
	for _, p := range []struct{ method, path, body string }{
		{"POST", "/v1/templates", `{"slug":"x"}`},
		{"GET", "/v1/templates/" + id, ""},
		{"POST", "/v1/templates/" + id + "/versions", `{"subject":"s","text":"t"}`},
		{"GET", "/v1/templates/" + id + "/versions/1", ""},
	} {
		if code, _ := s.do(t, p.method, p.path, emailsOnly, p.body); code != 403 {
			t.Errorf("emails-only %s %s: %d, want 403", p.method, p.path, code)
		}
	}
}

func TestPublishBodyLimits(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "templates:read", "templates:write")
	_, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"big"}`)
	id := body["id"].(string)

	// Maximum-size parts whose JSON escaping doubles the HTML still fit.
	big := map[string]any{
		"subject": "s",
		"text":    strings.Repeat("a", 512<<10),
		"html":    strings.Repeat(`"`, 512<<10),
	}
	if code, body := s.do(t, "POST", "/v1/templates/"+id+"/versions", token, jsonBody(t, big)); code != 201 {
		t.Fatalf("maximum version: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/templates/"+id+"/versions", token,
		`{"subject":"s","text":"`+strings.Repeat("a", 3<<20)+`"}`); code != 413 || errorCode(body) != "payload_too_large" {
		t.Fatalf("oversized request: %d %v", code, body)
	}
}
