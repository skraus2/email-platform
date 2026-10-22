package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/templates"
)

type fakeAutomations struct {
	err    error
	in     automations.CreateInput
	upd    automations.UpdateInput
	status string
	called bool
}

func (f *fakeAutomations) Create(_ context.Context, _ uuid.UUID, in automations.CreateInput) (automations.Automation, error) {
	f.in, f.called = in, true
	return automations.Automation{Status: "active", Steps: []automations.Step{{Type: automations.StepWait, Seconds: 60}}}, f.err
}

func (f *fakeAutomations) Get(context.Context, uuid.UUID, uuid.UUID) (automations.Automation, error) {
	return automations.Automation{Steps: []automations.Step{}, RunCounts: map[string]int{"active": 2}}, f.err
}

func (f *fakeAutomations) List(_ context.Context, _ uuid.UUID, status string, _ uuid.UUID, _ int) ([]automations.Automation, bool, error) {
	f.status = status
	return nil, false, f.err
}

func (f *fakeAutomations) Update(_ context.Context, _, _ uuid.UUID, in automations.UpdateInput) (automations.Automation, error) {
	f.upd, f.called = in, true
	return automations.Automation{Steps: []automations.Step{}}, f.err
}

var automationScopes = []string{auth.ScopeAutomationsRead, auth.ScopeAutomationsWrite}

func automationCall(t *testing.T, svc AutomationService, scopes []string, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := New(Deps{
		Logger: slog.New(slog.DiscardHandler),
		Authenticate: func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{ProjectID: uuid.New(), Scopes: scopes}, nil
		},
		Emails:      &fakeEmails{},
		Automations: svc,
	})
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer ep_test_x")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const validAutomationBody = `{"name":"Welcome","trigger":{"event":"user.signed_up"},"allow_reentry":true,"steps":[{"type":"wait","seconds":60}]}`

func TestAutomationErrorMapping(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{automations.ErrNotFound, 404, "not_found"},
		{automations.ErrTemplateNotFound, 422, "template_not_found"},
		{automations.ErrMissingUnsubscribe, 422, "template_missing_unsubscribe"},
		{automations.ErrLimit, 422, "automation_limit_reached"},
		{automations.ErrRateLimited, 429, "rate_limited"},
		{emails.ErrSenderNotVerified, 422, "sender_not_verified"},
		{automations.ErrArchived, 409, "automation_archived"},
		{&automations.Error{Field: "steps[0].seconds", Reason: "bad"}, 422, "validation_failed"},
		{&templates.Error{Code: templates.CodeVariablesInvalid, Field: "steps[0].variables.plan", Reason: "bad"}, 422, "variables_invalid"},
		{automations.ErrBusy, 503, "service_unavailable"},
		{errors.New("boom secret-detail"), 500, "internal_error"},
	} {
		for _, call := range []struct{ method, path, body string }{
			{"POST", "/v1/automations", validAutomationBody},
			{"GET", "/v1/automations/" + id, ""},
			{"GET", "/v1/automations", ""},
			{"PATCH", "/v1/automations/" + id, `{"status":"paused"}`},
		} {
			rec := automationCall(t, &fakeAutomations{err: tc.err}, automationScopes, call.method, call.path, "application/json", call.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) || strings.Contains(rec.Body.String(), "secret-detail") {
				t.Errorf("%v %s %s: %d %s", tc.err, call.method, call.path, rec.Code, rec.Body)
			}
			if tc.status == 429 && rec.Header().Get("Retry-After") != "60" {
				t.Errorf("%v %s %s: Retry-After %q", tc.err, call.method, call.path, rec.Header().Get("Retry-After"))
			}
		}
	}
}

func TestAutomationRequests(t *testing.T) {
	path := "/v1/automations/" + uuid.NewString()
	f := &fakeAutomations{}
	rec := automationCall(t, f, automationScopes, "POST", "/v1/automations", "application/json", validAutomationBody)
	if rec.Code != 201 || f.in.Name != "Welcome" || f.in.TriggerEvent != "user.signed_up" || !f.in.AllowReentry || !strings.HasPrefix(string(f.in.Steps), "[") {
		t.Fatalf("create: %d %+v", rec.Code, f.in)
	}
	if !strings.Contains(rec.Body.String(), `"steps":[{"type":"wait","seconds":60}]`) || strings.Contains(rec.Body.String(), "run_counts") {
		t.Fatalf("create response: %s", rec.Body)
	}
	for _, body := range []string{`{"x":1}`, `{"name":"a","trigger":{"event":"e","x":1},"steps":[]}`, `[]`} {
		f := &fakeAutomations{}
		if rec := automationCall(t, f, automationScopes, "POST", "/v1/automations", "application/json", body); rec.Code != 400 || f.called {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if f.in.Status != nil {
		t.Fatalf("absent status passed as %q", *f.in.Status)
	}
	f = &fakeAutomations{}
	if rec := automationCall(t, f, automationScopes, "POST", "/v1/automations", "application/json", strings.Replace(validAutomationBody, `"name"`, `"status":"","name"`, 1)); rec.Code != 201 || f.in.Status == nil || *f.in.Status != "" {
		t.Fatalf("empty status must reach validation: %d %+v", rec.Code, f.in.Status)
	}
	for _, p := range []string{"/v1/automations/nope", "/v1/automations/" + strings.Repeat("0", 36)} {
		f := &fakeAutomations{}
		if rec := automationCall(t, f, automationScopes, "GET", p, "", ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Errorf("malformed id %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	if rec := automationCall(t, &fakeAutomations{}, automationScopes, "POST", "/v1/automations", "text/plain", validAutomationBody); rec.Code != 415 {
		t.Errorf("content type: %d", rec.Code)
	}

	rec = automationCall(t, &fakeAutomations{}, automationScopes, "GET", path, "", "")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["run_counts"] == nil {
		t.Fatalf("get: %s", rec.Body)
	}
	if counts := got["run_counts"].(map[string]any); len(counts) != 5 || counts["active"] != 2.0 {
		t.Fatalf("run counts: %v", counts)
	}
	f = &fakeAutomations{}
	if rec := automationCall(t, f, automationScopes, "GET", "/v1/automations?status=paused", "", ""); rec.Code != 200 || f.status != "paused" {
		t.Fatalf("status filter: %d %q", rec.Code, f.status)
	}
	for _, q := range []string{"?status=draft", "?status=active&status=paused", "?x=1", "?limit=101"} {
		if rec := automationCall(t, &fakeAutomations{}, automationScopes, "GET", "/v1/automations"+q, "", ""); rec.Code != 400 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}

	f = &fakeAutomations{}
	if rec := automationCall(t, f, automationScopes, "PATCH", path, "application/json", `{"name":"New","status":"archived"}`); rec.Code != 200 ||
		f.upd.Name == nil || *f.upd.Name != "New" || f.upd.Status == nil || *f.upd.Status != "archived" {
		t.Fatalf("patch: %d %+v", rec.Code, f.upd)
	}
	for _, body := range []string{`{"status":null}`, `{"name":1}`} {
		f := &fakeAutomations{}
		if rec := automationCall(t, f, automationScopes, "PATCH", path, "application/json", body); rec.Code != 422 || f.called {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if rec := automationCall(t, &fakeAutomations{}, automationScopes, "PATCH", path, "application/json", `{"steps":[]}`); rec.Code != 400 {
		t.Errorf("steps are immutable: %d", rec.Code)
	}
	if rec := automationCall(t, &fakeAutomations{}, automationScopes, "DELETE", path, "", ""); rec.Code != 405 {
		t.Errorf("delete: %d", rec.Code)
	}

	// Scopes.
	if rec := automationCall(t, &fakeAutomations{}, []string{auth.ScopeAutomationsRead}, "POST", "/v1/automations", "application/json", validAutomationBody); rec.Code != 403 {
		t.Errorf("create with read scope: %d", rec.Code)
	}
	if rec := automationCall(t, &fakeAutomations{}, []string{auth.ScopeAutomationsRead}, "PATCH", path, "application/json", `{"status":"paused"}`); rec.Code != 403 {
		t.Errorf("patch with read scope: %d", rec.Code)
	}
	if rec := automationCall(t, &fakeAutomations{}, []string{auth.ScopeAutomationsWrite}, "GET", path, "", ""); rec.Code != 403 {
		t.Errorf("get with write scope: %d", rec.Code)
	}
}
