package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/webhooks"
)

// fakeRegistrar records its calls and answers with a fixed result.
type fakeRegistrar struct {
	calls     int
	projectID uuid.UUID
	url       string
	types     []string
	endpoint  webhooks.Endpoint
	secret    string
	err       error
}

func (f *fakeRegistrar) Create(_ context.Context, projectID uuid.UUID, rawURL string, types []string) (webhooks.Endpoint, string, error) {
	f.calls++
	f.projectID, f.url, f.types = projectID, rawURL, types
	return f.endpoint, f.secret, f.err
}

var hookPrincipal = auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeWebhooksWrite}}

// webhookCall posts body to /v1/webhook-endpoints. token "none" sends no
// Authorization header; "noscope" authenticates without webhooks:write.
func webhookCall(t *testing.T, reg WebhookRegistrar, token, contentType, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logs bytes.Buffer
	s := New(Deps{
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		Authenticate: func(_ context.Context, tok string) (auth.Principal, error) {
			if tok == "ep_noscope_x" {
				return auth.Principal{ProjectID: hookPrincipal.ProjectID, Scopes: []string{auth.ScopeEmailsSend, auth.ScopeTemplatesWrite}}, nil
			}
			return hookPrincipal, nil
		},
		Emails:   &fakeEmails{},
		Webhooks: reg,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/webhook-endpoints", strings.NewReader(body))
	switch token {
	case "none":
	case "noscope":
		req.Header.Set("Authorization", "Bearer ep_noscope_x")
	default:
		req.Header.Set("Authorization", "Bearer ep_test_x")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec, logs.String()
}

func TestCreateEndpointDisabled(t *testing.T) {
	for _, tc := range []struct {
		name, token, contentType, body string
		status                         int
		code                           string
	}{
		{"no key", "none", "application/json", `{"url":"https://a.example/"}`, 401, "unauthorized"},
		{"missing scope", "noscope", "application/json", `{"url":"https://a.example/"}`, 403, "forbidden"},
		{"disabled", "", "application/json", `{"url":"https://a.example/"}`, 503, "webhook_registration_disabled"},
		// The body is never looked at while registration is off.
		{"disabled before media type", "", "text/plain", `nonsense`, 503, "webhook_registration_disabled"},
		{"disabled before decoding", "", "application/json", `{"bogus":1}`, 503, "webhook_registration_disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := webhookCall(t, nil, tc.token, tc.contentType, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body)
			}
			assertErrorCode(t, rec, tc.code)
			if tc.status == 503 && rec.Header().Get("Retry-After") != "" {
				t.Errorf("Retry-After = %q, want none: retrying does not help", rec.Header().Get("Retry-After"))
			}
		})
	}
}

func TestCreateEndpointScopeChecked(t *testing.T) {
	reg := &fakeRegistrar{}
	rec, _ := webhookCall(t, reg, "noscope", "application/json", `{"url":"https://a.example/"}`)
	if rec.Code != 403 || reg.calls != 0 {
		t.Fatalf("status = %d, calls = %d; want 403 without a call", rec.Code, reg.calls)
	}
}

func TestCreateEndpointCreated(t *testing.T) {
	created := time.Date(2026, 10, 18, 9, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	id := uuid.New()
	for _, tc := range []struct {
		name, body string
		wantTypes  []string
	}{
		{"all types by default", `{"url":"https://hooks.example.test/in?token=t0k3n"}`, nil},
		{"subset", `{"url":"https://hooks.example.test/in?token=t0k3n","event_types":["email.bounced","email.complained"]}`,
			[]string{"email.bounced", "email.complained"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			types := tc.wantTypes
			if types == nil {
				types = webhooks.EventTypes
			}
			reg := &fakeRegistrar{
				endpoint: webhooks.Endpoint{
					ID: id, ProjectID: hookPrincipal.ProjectID, URL: "https://hooks.example.test/in?token=t0k3n",
					EventTypes: types, Status: "active", KeyID: "k1", CreatedAt: created,
				},
				secret: "whsec_c2VjcmV0LXZhbHVlLWZvci10ZXN0",
			}
			rec, logs := webhookCall(t, reg, "", "application/json; charset=utf-8", tc.body)
			if rec.Code != 201 {
				t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
			}
			if reg.calls != 1 || reg.projectID != hookPrincipal.ProjectID || reg.url != "https://hooks.example.test/in?token=t0k3n" ||
				!slices.Equal(reg.types, tc.wantTypes) {
				t.Fatalf("registrar got calls=%d project=%s url=%q types=%v", reg.calls, reg.projectID, reg.url, reg.types)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"id": id.String(), "url": "https://hooks.example.test/in?token=t0k3n", "status": "active",
				"created_at": "2026-10-18T07:30:00Z", "secret": "whsec_c2VjcmV0LXZhbHVlLWZvci10ZXN0",
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %v, want %v", k, got[k], v)
				}
			}
			gotTypes, _ := got["event_types"].([]any)
			if fmt.Sprint(gotTypes) != fmt.Sprint(types) {
				t.Errorf("event_types = %v, want %v", gotTypes, types)
			}
			if len(got) != 6 {
				t.Errorf("response has %d fields, want 6: %v", len(got), got)
			}
			for _, leak := range []string{"t0k3n", "hooks.example.test", "whsec_", "c2VjcmV0"} {
				if strings.Contains(logs, leak) {
					t.Errorf("logs contain %q: %s", leak, logs)
				}
			}
			if !strings.Contains(logs, id.String()) || !strings.Contains(logs, hookPrincipal.ProjectID.String()) {
				t.Errorf("logs miss endpoint or project id: %s", logs)
			}
		})
	}
}

func TestCreateEndpointRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		code                    string
	}{
		{"media type", "text/plain", `{"url":"https://a.example/"}`, 415, "unsupported_media_type"},
		{"unknown field", "application/json", `{"url":"https://a.example/","project_id":"x"}`, 400, "invalid_json"},
		{"url not a string", "application/json", `{"url":5}`, 400, "invalid_json"},
		{"types not an array", "application/json", `{"url":"https://a.example/","event_types":"email.bounced"}`, 400, "invalid_json"},
		{"trailing data", "application/json", `{"url":"https://a.example/"} {}`, 400, "invalid_json"},
		{"not an object", "application/json", `[]`, 400, "invalid_json"},
		{"too large", "application/json", `{"url":"https://a.example/` + strings.Repeat("a", maxRequestBytes) + `"}`, 413, "payload_too_large"},
		{"null types", "application/json", `{"url":"https://a.example/","event_types":null}`, 422, "validation_failed"},
		{"empty types", "application/json", `{"url":"https://a.example/","event_types":[]}`, 422, "validation_failed"},
		{"duplicate types", "application/json", `{"url":"https://a.example/","event_types":["email.failed","email.failed"]}`, 422, "validation_failed"},
		{"unknown type", "application/json", `{"url":"https://a.example/","event_types":["email.opened<script>"]}`, 422, "validation_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &fakeRegistrar{}
			rec, _ := webhookCall(t, reg, "", tc.contentType, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body)
			}
			body := rec.Body.String()
			assertErrorCode(t, rec, tc.code)
			if reg.calls != 0 {
				t.Errorf("registrar called for an invalid request")
			}
			if strings.Contains(body, "opened") || strings.Contains(body, "script") {
				t.Errorf("error echoes the submitted value: %s", body)
			}
			if tc.code == "validation_failed" && !strings.Contains(body, "event_types") {
				t.Errorf("message does not name the field: %s", body)
			}
		})
	}
}

func TestCreateEndpointErrorMapping(t *testing.T) {
	forbidden := fmt.Errorf("%w: scheme must be https", webhooks.ErrForbiddenTarget)
	for _, tc := range []struct {
		name       string
		err        error
		status     int
		code       string
		retryAfter bool
		message    string
	}{
		{"forbidden target", forbidden, 422, "invalid_url", false, "scheme must be https"},
		{"unknown type", fmt.Errorf("%w: %q", webhooks.ErrUnknownEventType, "email.secret-value"), 422, "validation_failed", false, "event_types"},
		{"limit", webhooks.ErrEndpointLimit, 422, "endpoint_limit_reached", false, "limit"},
		{"busy", fmt.Errorf("create: %w", webhooks.ErrBusy), 503, "service_unavailable", true, ""},
		{"database down", fmt.Errorf("x: %w", &pgconn.PgError{Code: "57P03"}), 503, "service_unavailable", true, ""},
		{"unexpected", errors.New("boom https://hooks.example.test/in?token=t0k3n"), 500, "internal_error", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, logs := webhookCall(t, &fakeRegistrar{err: tc.err}, "", "application/json",
				`{"url":"https://hooks.example.test/in?token=t0k3n"}`)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body)
			}
			body := rec.Body.String()
			assertErrorCode(t, rec, tc.code)
			if (rec.Header().Get("Retry-After") != "") != tc.retryAfter {
				t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
			}
			if !strings.Contains(body, tc.message) {
				t.Errorf("body %s does not contain %q", body, tc.message)
			}
			for _, leak := range []string{"secret-value", "t0k3n"} {
				if strings.Contains(body, leak) {
					t.Errorf("body leaks %q: %s", leak, body)
				}
			}
			if strings.Contains(logs, "t0k3n") {
				t.Errorf("logs contain the URL token: %s", logs)
			}
		})
	}
}

func TestCreateEndpointMethods(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, "GET", "/v1/webhook-endpoints", nil)
	if rec.Code != 405 || rec.Header().Get("Allow") != "POST, HEAD" {
		t.Fatalf("GET: %d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}
