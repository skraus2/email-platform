package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
)

type getEmails struct {
	fakeEmails
	email emails.Email
	err   error
	asked uuid.UUID
}

func (g *getEmails) Get(_ context.Context, projectID, id uuid.UUID) (emails.Email, error) {
	g.asked = projectID
	if g.err != nil {
		return emails.Email{}, g.err
	}
	if id != g.email.ID || projectID != g.email.ProjectID {
		return emails.Email{}, emails.ErrNotFound
	}
	return g.email, nil
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGetEmailReturnsOwnEmail(t *testing.T) {
	created := time.Date(2026, 10, 13, 12, 0, 0, 123, time.UTC)
	finished := created.Add(time.Second)
	e := emails.Email{
		ID: uuid.New(), ProjectID: principal.ProjectID, Status: "captured", From: "a@example.test",
		To: "b@example.test", Subject: "Hi", CreatedAt: created, UpdatedAt: finished, FinishedAt: &finished,
		Attempts: 2,
	}
	svc := &getEmails{email: e}
	s := New(Deps{Logger: quietLogger(), Authenticate: fixedAuth(principal), Emails: svc})
	rec := get(t, s, "/v1/emails/"+e.ID.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	want := map[string]any{
		"id": e.ID.String(), "status": "captured", "from": "a@example.test", "to": "b@example.test",
		"subject": "Hi", "created_at": "2026-10-13T12:00:00.000000123Z",
		"updated_at": "2026-10-13T12:00:01.000000123Z", "finished_at": "2026-10-13T12:00:01.000000123Z",
		"attempts": float64(2), "error_code": nil,
	}
	if len(body) != len(want) {
		t.Fatalf("body = %v", body)
	}
	for k, v := range want {
		if got, ok := body[k]; !ok || got != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
}

func TestGetEmailUnfinishedHasNullFinishedAt(t *testing.T) {
	e := emails.Email{ID: uuid.New(), ProjectID: principal.ProjectID, Status: "queued"}
	s := New(Deps{Logger: quietLogger(), Authenticate: fixedAuth(principal), Emails: &getEmails{email: e}})
	body := decode(t, get(t, s, "/v1/emails/"+e.ID.String()))
	if v, ok := body["finished_at"]; !ok || v != nil {
		t.Fatalf("finished_at = %v (present %v), want null", v, ok)
	}
}

func TestGetEmailShowsErrorCode(t *testing.T) {
	code := emails.CodeProviderUnreachable
	e := emails.Email{ID: uuid.New(), ProjectID: principal.ProjectID, Status: "queued", Attempts: 1, ErrorCode: &code}
	s := New(Deps{Logger: quietLogger(), Authenticate: fixedAuth(principal), Emails: &getEmails{email: e}})
	body := decode(t, get(t, s, "/v1/emails/"+e.ID.String()))
	if body["error_code"] != "provider_unreachable" || body["attempts"] != float64(1) {
		t.Fatalf("body = %v, want error_code provider_unreachable and attempts 1", body)
	}
	if _, ok := body["last_error"]; ok {
		t.Fatal("internal error detail exposed")
	}
}

func TestGetEmailNotFoundCasesAreIdentical(t *testing.T) {
	own := emails.Email{ID: uuid.New(), ProjectID: uuid.New()} // belongs to another project
	svc := &getEmails{email: own}
	s := New(Deps{Logger: quietLogger(), Authenticate: fixedAuth(principal), Emails: svc})
	var bodies []string
	for _, path := range []string{
		"/v1/emails/" + own.ID.String(),     // foreign project
		"/v1/emails/" + uuid.NewString(),    // unknown
		"/v1/emails/not-a-uuid",             // malformed
		"/v1/emails/" + own.ID.String()[:8], // truncated
	} {
		rec := get(t, s, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: code = %d", path, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Fatalf("404 bodies differ: %q vs %q", bodies[0], b)
		}
	}
	if svc.asked != principal.ProjectID {
		t.Fatal("lookup not scoped to the caller's project")
	}
}

func TestGetEmailRequiresReadScope(t *testing.T) {
	sendOnly := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsSend}}
	s := New(Deps{Logger: quietLogger(), Authenticate: fixedAuth(sendOnly), Emails: &getEmails{}})
	if rec := get(t, s, "/v1/emails/"+uuid.NewString()); rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
}

func TestGetEmailInternalError(t *testing.T) {
	s := New(Deps{Logger: quietLogger(), Authenticate: fixedAuth(principal), Emails: &getEmails{err: errors.New("db down")}})
	rec := get(t, s, "/v1/emails/"+uuid.NewString())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
	assertErrorCode(t, rec, "internal_error")
}
