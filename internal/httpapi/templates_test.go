package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/templates"
)

type failingTemplates struct{ err error }

func (f failingTemplates) Create(context.Context, uuid.UUID, string, string) (templates.Template, error) {
	return templates.Template{}, f.err
}

func (f failingTemplates) Publish(context.Context, uuid.UUID, uuid.UUID, templates.Source, []byte) (templates.Version, error) {
	return templates.Version{}, f.err
}

func (f failingTemplates) Get(context.Context, uuid.UUID, uuid.UUID) (templates.Template, error) {
	return templates.Template{}, f.err
}

func (f failingTemplates) GetVersion(context.Context, uuid.UUID, uuid.UUID, int) (templates.Version, error) {
	return templates.Version{}, f.err
}

var templatePrincipal = auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeTemplatesRead, auth.ScopeTemplatesWrite}}

func templateCall(t *testing.T, svc TemplateService, method, path, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logs bytes.Buffer
	s := New(Deps{
		Logger:       slog.New(slog.NewJSONHandler(&logs, nil)),
		Authenticate: func(context.Context, string) (auth.Principal, error) { return templatePrincipal, nil },
		Emails:       &fakeEmails{},
		Templates:    svc,
	})
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer ep_test_x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec, logs.String()
}

func TestTemplateErrorMapping(t *testing.T) {
	id := uuid.NewString()
	publish := `{"subject":"s","text":"t"}`
	for _, tc := range []struct {
		err     error
		status  int
		errCode string
	}{
		{templates.ErrNotFound, 404, "not_found"},
		{templates.ErrSlugTaken, 409, "slug_taken"},
		{templates.ErrTemplateLimit, 422, "template_limit_reached"},
		{templates.ErrVersionLimit, 422, "version_limit_reached"},
		{templates.ErrStorageLimit, 422, "template_storage_limit_reached"},
		{&templates.Error{Code: templates.CodeTemplateInvalid, Field: "html", Reason: "line 2: nope"}, 422, "template_invalid"},
		{&templates.Error{Code: templates.CodeValidation, Field: "variables.x", Reason: "bad"}, 422, "validation_failed"},
		{fmt.Errorf("x: %w", &pgconn.PgError{Code: "57P03"}), 503, "service_unavailable"},
		{errors.New("boom secret-detail"), 500, "internal_error"},
		{templates.ErrBusy, 503, "service_unavailable"},
		{&templates.RateLimitError{RetryAfter: 17 * time.Second}, 429, "rate_limited"},
	} {
		for _, call := range []struct{ method, path, body string }{
			{"POST", "/v1/templates", `{"slug":"a"}`},
			{"POST", "/v1/templates/" + id + "/versions", publish},
			{"GET", "/v1/templates/" + id, ""},
			{"GET", "/v1/templates/" + id + "/versions/1", ""},
		} {
			rec, logs := templateCall(t, failingTemplates{tc.err}, call.method, call.path, call.body)
			if rec.Code != tc.status {
				t.Errorf("%v on %s %s: %d, want %d", tc.err, call.method, call.path, rec.Code, tc.status)
				continue
			}
			assertErrorCode(t, rec, tc.errCode)
			if tc.status == 429 && rec.Header().Get("Retry-After") != "17" {
				t.Errorf("Retry-After = %q, want 17", rec.Header().Get("Retry-After"))
			}
			if tc.status == 503 && rec.Header().Get("Retry-After") == "" {
				t.Error("503 without Retry-After")
			}
			if strings.Contains(rec.Body.String(), "secret-detail") {
				t.Errorf("leaks internal error: %s", rec.Body)
			}
			if tc.status == 500 && !strings.Contains(logs, "secret-detail") {
				t.Errorf("internal error not logged")
			}
		}
	}
}

func TestTemplateRoutesRejectMalformedPaths(t *testing.T) {
	called := failingTemplates{errors.New("service must not be called")}
	for _, path := range []string{
		"/v1/templates/not-a-uuid",
		"/v1/templates/" + uuid.NewString() + "/versions/0",
		"/v1/templates/" + uuid.NewString() + "/versions/01",
		"/v1/templates/" + uuid.NewString() + "/versions/-1",
		"/v1/templates/" + uuid.NewString() + "/versions/abc",
		"/v1/templates/" + uuid.NewString() + "/versions/99999999999999999999",
		"/v1/templates/not-a-uuid/versions/1",
	} {
		rec, _ := templateCall(t, called, "GET", path, "")
		if rec.Code != 404 {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
	rec, _ := templateCall(t, called, "POST", "/v1/templates/not-a-uuid/versions", `{"subject":"s","text":"t"}`)
	if rec.Code != 404 {
		t.Errorf("publish to malformed id: %d", rec.Code)
	}
	for _, m := range []struct{ method, path string }{
		{"GET", "/v1/templates"}, {"DELETE", "/v1/templates/" + uuid.NewString()},
		{"GET", "/v1/templates/" + uuid.NewString() + "/versions"}, {"PUT", "/v1/templates/" + uuid.NewString() + "/versions/1"},
	} {
		if rec, _ := templateCall(t, called, m.method, m.path, ""); rec.Code != 405 {
			t.Errorf("%s %s: %d, want 405", m.method, m.path, rec.Code)
		}
	}
}

func TestRequestFieldsAreExactAndUnique(t *testing.T) {
	for _, body := range []string{
		`{"slug":"a","SLUG":"b"}`,
		`{"Slug":"a"}`,
		`{"slug":"a","slug":"b"}`,
		`{"sl\u0075g":"a","slug":"b"}`,
	} {
		rec, _ := templateCall(t, failingTemplates{errors.New("service must not be called")}, "POST", "/v1/templates", body)
		if rec.Code != 400 {
			t.Errorf("%s: %d, want 400", body, rec.Code)
			continue
		}
		assertErrorCode(t, rec, "invalid_json")
	}
}
