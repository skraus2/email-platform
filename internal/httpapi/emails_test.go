package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
)

type fakeEmails struct {
	created   []emails.CreateInput
	projectID uuid.UUID
	err       error
	replayed  bool
}

func (f *fakeEmails) Create(_ context.Context, projectID uuid.UUID, in emails.CreateInput) (emails.Email, error) {
	if f.err != nil {
		return emails.Email{}, f.err
	}
	if err := in.Validate(); err != nil {
		return emails.Email{}, err
	}
	f.created = append(f.created, in)
	f.projectID = projectID
	status := "queued"
	if f.replayed {
		status = "captured" // a replay names an email that has moved on
	}
	return emails.Email{ID: uuid.MustParse("01a13601-4b3e-72cd-9dcd-4d9159938763"), ProjectID: projectID, Status: status,
		CreatedAt: time.Now(), Replayed: f.replayed}, nil
}

func (f *fakeEmails) Get(context.Context, uuid.UUID, uuid.UUID) (emails.Email, error) {
	return emails.Email{}, emails.ErrNotFound
}

var principal = auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsSend, auth.ScopeEmailsRead}}

func emailServer(t *testing.T, svc *fakeEmails, p auth.Principal) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	return New(Deps{
		Logger:       slog.New(slog.NewJSONHandler(&logs, nil)),
		Authenticate: func(context.Context, string) (auth.Principal, error) { return p, nil },
		Emails:       svc,
	}), &logs
}

func post(t *testing.T, s *Server, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/emails", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const okBody = `{"from":"noreply@example.test","to":"customer@example.test","subject":"Hi","text":"Hello","html":"<p>Hello</p>"}`

func TestCreateEmailAccepted(t *testing.T) {
	svc := &fakeEmails{}
	s, logs := emailServer(t, svc, principal)
	rec := post(t, s, okBody, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["status"] != "queued" || body["id"] != "01a13601-4b3e-72cd-9dcd-4d9159938763" || len(body) != 2 {
		t.Fatalf("body = %v", body)
	}
	if svc.projectID != principal.ProjectID {
		t.Fatal("email created for the wrong project")
	}
	want := emails.CreateInput{From: "noreply@example.test", To: "customer@example.test", Subject: "Hi", Text: "Hello", HTML: "<p>Hello</p>"}
	if svc.created[0] != want {
		t.Fatalf("input = %+v", svc.created[0])
	}
	if strings.Contains(logs.String(), "customer@example.test") || strings.Contains(logs.String(), "Hello") {
		t.Errorf("request log contains recipient or body: %s", logs.String())
	}
}

func TestCreateEmailRejects(t *testing.T) {
	big := `{"from":"a@example.test","to":"b@example.test","subject":"s","text":"` + strings.Repeat("a", 1<<20) + `"}`
	tests := []struct {
		name   string
		body   string
		hdr    map[string]string
		status int
		code   string
	}{
		{"malformed json", `{"from":`, nil, 400, "invalid_json"},
		{"unknown field", `{"from":"a@example.test","to":"b@example.test","subject":"s","text":"t","cc":"c@example.test"}`, nil, 400, "invalid_json"},
		{"wrong type", `{"from":"a@example.test","to":["b@example.test"],"subject":"s","text":"t"}`, nil, 400, "invalid_json"},
		{"trailing data", okBody + `{}`, nil, 400, "invalid_json"},
		{"empty body", ``, nil, 400, "invalid_json"},
		{"idempotency key with space", okBody, map[string]string{"Idempotency-Key": "a b"}, 400, "invalid_idempotency_key"},
		{"idempotency key too long", okBody, map[string]string{"Idempotency-Key": strings.Repeat("k", 256)}, 400, "invalid_idempotency_key"},
		{"idempotency key non-ascii", okBody, map[string]string{"Idempotency-Key": "schlüssel"}, 400, "invalid_idempotency_key"},
		{"no content type", okBody, map[string]string{"Content-Type": ""}, 415, "unsupported_media_type"},
		{"form content type", okBody, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415, "unsupported_media_type"},
		{"too large", big, nil, 413, "payload_too_large"},
		{"template id", `{"from":"a@example.test","to":"b@example.test","subject":"s","template_id":"t1"}`, nil, 422, "templates_not_supported"},
		{"template null", `{"from":"a@example.test","to":"b@example.test","subject":"s","text":"t","variables":null}`, nil, 422, "templates_not_supported"},
		{"invalid address", `{"from":"a@example.test","to":"not-an-address","subject":"s","text":"t"}`, nil, 422, "validation_failed"},
		{"header injection", `{"from":"a@example.test","to":"b@example.test","subject":"Hi\r\nBcc: x@example.test","text":"t"}`, nil, 422, "validation_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeEmails{}
			s, _ := emailServer(t, svc, principal)
			rec := post(t, s, tt.body, tt.hdr)
			if rec.Code != tt.status {
				t.Fatalf("code = %d, want %d (body %s)", rec.Code, tt.status, rec.Body)
			}
			assertErrorCode(t, rec, tt.code)
			if len(svc.created) != 0 {
				t.Fatal("email created for a rejected request")
			}
		})
	}
}

func TestCreateEmailValidationMessageNamesField(t *testing.T) {
	s, _ := emailServer(t, &fakeEmails{}, principal)
	rec := post(t, s, `{"from":"a@example.test","to":"nope","subject":"s","text":"t"}`, nil)
	body := decode(t, rec)
	msg, _ := body["error"].(map[string]any)["message"].(string)
	if !strings.HasPrefix(msg, "to:") {
		t.Fatalf("message = %q, want it to name the field", msg)
	}
}

func TestCreateEmailRequiresSendScope(t *testing.T) {
	readOnly := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsRead}}
	svc := &fakeEmails{}
	s, _ := emailServer(t, svc, readOnly)
	rec := post(t, s, okBody, nil)
	if rec.Code != http.StatusForbidden || len(svc.created) != 0 {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestCreateEmailInternalErrorDoesNotLeak(t *testing.T) {
	s, logs := emailServer(t, &fakeEmails{err: errors.New("pq: relation emails at 10.0.0.5 broken")}, principal)
	rec := post(t, s, okBody, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
	assertErrorCode(t, rec, "internal_error")
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatal("internal error leaked")
	}
	if !strings.Contains(logs.String(), "create email failed") {
		t.Fatal("internal error not logged")
	}
}

func TestEmailsWrongMethod(t *testing.T) {
	s, _ := emailServer(t, &fakeEmails{}, principal)
	req := httptest.NewRequest(http.MethodDelete, "/v1/emails", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}

func TestCreateEmailPassesIdempotencyKey(t *testing.T) {
	svc := &fakeEmails{}
	s, logs := emailServer(t, svc, principal)
	rec := post(t, s, okBody, map[string]string{"Idempotency-Key": "welcome-user-123"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	if svc.created[0].IdempotencyKey != "welcome-user-123" {
		t.Fatalf("key = %q", svc.created[0].IdempotencyKey)
	}
	if rec.Header().Get("Idempotent-Replayed") != "" {
		t.Error("first request marked as replay")
	}
	if strings.Contains(logs.String(), "welcome-user-123") {
		t.Error("idempotency key value logged")
	}
}

func TestCreateEmailReplay(t *testing.T) {
	s, _ := emailServer(t, &fakeEmails{replayed: true}, principal)
	rec := post(t, s, okBody, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusAccepted || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("code = %d, Idempotent-Replayed = %q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
	// The replay is the original response: status queued, whatever the
	// email's current status is.
	body := decode(t, rec)
	if body["status"] != "queued" || body["id"] != "01a13601-4b3e-72cd-9dcd-4d9159938763" || len(body) != 2 {
		t.Fatalf("body = %v", body)
	}
}

func TestCreateEmailIdempotencyConflict(t *testing.T) {
	s, _ := emailServer(t, &fakeEmails{err: emails.ErrIdempotencyKeyReused}, principal)
	rec := post(t, s, okBody, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409", rec.Code)
	}
	assertErrorCode(t, rec, "idempotency_key_reused")
}

func TestCreateEmailRejectsEmptyOrRepeatedIdempotencyKey(t *testing.T) {
	for name, values := range map[string][]string{"empty": {""}, "repeated": {"k1", "k2"}, "repeated same": {"k1", "k1"}} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeEmails{}
			s, _ := emailServer(t, svc, principal)
			req := httptest.NewRequest(http.MethodPost, "/v1/emails", strings.NewReader(okBody))
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header["Idempotency-Key"] = values
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", rec.Code)
			}
			assertErrorCode(t, rec, "invalid_idempotency_key")
			if len(svc.created) != 0 {
				t.Fatal("email created")
			}
		})
	}
}

func TestCreateEmailRateLimited(t *testing.T) {
	s, _ := emailServer(t, &fakeEmails{err: &emails.RateLimitError{RetryAfter: 17 * time.Second}}, principal)
	rec := post(t, s, okBody, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "17" {
		t.Fatalf("code = %d, Retry-After = %q; want 429 and 17", rec.Code, rec.Header().Get("Retry-After"))
	}
	assertErrorCode(t, rec, "rate_limited")
}
