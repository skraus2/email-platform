package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, checks ...Check) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	return New(logger, checks), &logs
}

func do(t *testing.T, s *Server, method, target string, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, resp *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := resp.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func okCheck(name string) Check {
	return Check{Name: name, Fn: func(context.Context) error { return nil }}
}

func TestHealthzIsIndependentOfDependencies(t *testing.T) {
	failing := Check{Name: "database", Fn: func(context.Context) error { return errors.New("down") }}
	s, _ := newTestServer(t, failing)

	resp := do(t, s, http.MethodGet, "/healthz", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even with failing dependency", resp.Code)
	}
	if body := decode(t, resp); body["status"] != "ok" {
		t.Errorf("body = %v", body)
	}
}

func TestReadyzAllChecksPass(t *testing.T) {
	s, _ := newTestServer(t, okCheck("database"))

	resp := do(t, s, http.MethodGet, "/readyz", nil)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.Code)
	}
	body := decode(t, resp)
	if body["status"] != "ready" {
		t.Errorf("status = %v, want ready", body["status"])
	}
	if checks := body["checks"].(map[string]any); checks["database"] != "ok" {
		t.Errorf("checks = %v", checks)
	}
}

func TestReadyzFailingCheckDoesNotLeakError(t *testing.T) {
	secret := "password=s3cret host=10.0.0.5"
	failing := Check{Name: "database", Fn: func(context.Context) error { return errors.New(secret) }}
	s, logs := newTestServer(t, okCheck("other"), failing)

	resp := do(t, s, http.MethodGet, "/readyz", nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.Code)
	}
	raw := resp.Body.Bytes()
	if strings.Contains(string(raw), "s3cret") || strings.Contains(string(raw), "10.0.0.5") {
		t.Fatalf("readyz body leaks check error: %s", raw)
	}
	var body readyResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "not_ready" || body.Checks["database"] != "unavailable" || body.Checks["other"] != "ok" {
		t.Errorf("body = %+v", body)
	}
	if !strings.Contains(logs.String(), "readiness check failed") {
		t.Errorf("failure was not logged for operators")
	}
}

func TestReadyzChecksAreBoundedByTimeout(t *testing.T) {
	var sawDeadline bool
	c := Check{Name: "database", Fn: func(ctx context.Context) error {
		_, sawDeadline = ctx.Deadline()
		return nil
	}}
	s, _ := newTestServer(t, c)
	do(t, s, http.MethodGet, "/readyz", nil)
	if !sawDeadline {
		t.Error("readiness check context has no deadline")
	}
}

func TestReadyzWhileDraining(t *testing.T) {
	called := false
	c := Check{Name: "database", Fn: func(context.Context) error { called = true; return nil }}
	s, _ := newTestServer(t, c)
	s.SetDraining()

	resp := do(t, s, http.MethodGet, "/readyz", nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while draining", resp.Code)
	}
	if called {
		t.Error("checks should be skipped while draining")
	}
	// Liveness stays green during graceful shutdown.
	if resp := do(t, s, http.MethodGet, "/healthz", nil); resp.Code != http.StatusOK {
		t.Errorf("healthz = %d while draining, want 200", resp.Code)
	}
}

func TestUnknownRouteReturnsErrorEnvelope(t *testing.T) {
	s, _ := newTestServer(t)
	resp := do(t, s, http.MethodGet, "/v1/emails", nil)
	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.Code)
	}
	body := decode(t, resp)
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj["code"] != "not_found" || errObj["message"] == "" {
		t.Errorf("body = %v, want error envelope", body)
	}
}

func TestHealthEndpointsRejectWrongMethod(t *testing.T) {
	s, _ := newTestServer(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		resp := do(t, s, http.MethodPost, path, nil)
		if resp.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, resp.Code)
		}
	}
}

func TestRequestID(t *testing.T) {
	s, logs := newTestServer(t)

	resp := do(t, s, http.MethodGet, "/healthz", http.Header{requestIDHeader: {"abc-123"}})
	if got := resp.Header().Get(requestIDHeader); got != "abc-123" {
		t.Errorf("valid caller request id not echoed: %q", got)
	}

	resp = do(t, s, http.MethodGet, "/healthz", http.Header{requestIDHeader: {"bad id\n{\"x\":1}"}})
	got := resp.Header().Get(requestIDHeader)
	if got == "" || strings.ContainsAny(got, " \n{") {
		t.Errorf("unsafe request id not replaced: %q", got)
	}
	if !strings.Contains(logs.String(), `"request_id":"abc-123"`) {
		t.Errorf("request id not logged: %s", logs.String())
	}
}

func TestRequestLogOmitsQueryString(t *testing.T) {
	s, logs := newTestServer(t)
	do(t, s, http.MethodGet, "/healthz?token=s3cret", nil)
	if strings.Contains(logs.String(), "s3cret") {
		t.Errorf("query string leaked into logs: %s", logs.String())
	}
}
