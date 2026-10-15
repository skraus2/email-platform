package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
)

type listEmails struct {
	fakeEmails
	all          []emails.Email // newest first
	askedProject uuid.UUID
	askedFilter  emails.ListFilter
	askedLimit   int
	err          error
}

func (l *listEmails) List(_ context.Context, projectID uuid.UUID, f emails.ListFilter, limit int) ([]emails.Email, bool, error) {
	l.askedProject, l.askedFilter, l.askedLimit = projectID, f, limit
	if l.err != nil {
		return nil, false, l.err
	}
	start := 0
	for i, e := range l.all {
		if e.ID == f.After {
			start = i + 1
		}
	}
	rest := l.all[start:]
	if len(rest) > limit {
		return rest[:limit], true, nil
	}
	return rest, false, nil
}

type emailListBody struct {
	Data       []map[string]any `json:"data"`
	NextCursor *string          `json:"next_cursor"`
}

func listEmailsServer(t *testing.T, p auth.Principal) (*Server, *listEmails) {
	t.Helper()
	svc := &listEmails{}
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	code := "provider_rejected"
	for i := range 3 {
		e := emails.Email{
			ID: uuid.Must(uuid.NewV7()), ProjectID: p.ProjectID, Status: "captured",
			From: "noreply@example.test", To: fmt.Sprintf("user%d@example.test", i), Subject: "Hi",
			CreatedAt: at, UpdatedAt: at, Attempts: 1,
		}
		if i == 2 {
			e.Status, e.ErrorCode = "failed", &code
		}
		svc.all = append(svc.all, e)
	}
	s, _ := emailServer(t, &svc.fakeEmails, p)
	s.emails = svc
	return s, svc
}

func getList(t *testing.T, s *Server, path string) (*httptest.ResponseRecorder, emailListBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer ep_test_x")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body emailListBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return rec, body
}

func TestListEmailsPaginates(t *testing.T) {
	s, svc := listEmailsServer(t, principal)
	rec, page1 := getList(t, s, "/v1/emails?limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if svc.askedProject != principal.ProjectID {
		t.Fatalf("listed project %s, want the key's project", svc.askedProject)
	}
	if len(page1.Data) != 2 || page1.NextCursor == nil {
		t.Fatalf("page 1: %d items, cursor %v", len(page1.Data), page1.NextCursor)
	}
	if page1.Data[0]["id"] != svc.all[0].ID.String() || page1.Data[0]["to"] != "user0@example.test" {
		t.Fatalf("first item %v", page1.Data[0])
	}
	if page1.Data[0]["created_at"] != "2026-10-15T07:00:00Z" {
		t.Fatalf("created_at not UTC: %v", page1.Data[0]["created_at"])
	}
	for _, k := range []string{"status", "from", "subject", "attempts", "error_code", "updated_at", "finished_at"} {
		if _, ok := page1.Data[0][k]; !ok {
			t.Errorf("item lacks %q", k)
		}
	}
	rec, page2 := getList(t, s, "/v1/emails?limit=2&cursor="+*page1.NextCursor)
	if rec.Code != http.StatusOK || svc.askedFilter.After != svc.all[1].ID {
		t.Fatalf("cursor decoded to %s, want %s (code %d)", svc.askedFilter.After, svc.all[1].ID, rec.Code)
	}
	if len(page2.Data) != 1 || page2.NextCursor != nil || page2.Data[0]["error_code"] != "provider_rejected" {
		t.Fatalf("page 2: %+v", page2)
	}
	if _, _ = getList(t, s, "/v1/emails"); svc.askedLimit != defaultEmailPage {
		t.Fatalf("default limit = %d", svc.askedLimit)
	}
}

func TestListEmailsEmptyPageIsAnArray(t *testing.T) {
	s, svc := listEmailsServer(t, principal)
	svc.all = nil
	rec, _ := getList(t, s, "/v1/emails")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"data":[]`) ||
		!strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestListEmailsPassesFilters(t *testing.T) {
	s, svc := listEmailsServer(t, principal)
	rec, _ := getList(t, s, "/v1/emails?status=failed&status=bounced&recipient=Customer@Example.test"+
		"&created_after=2026-10-01T00:00:00Z&created_before=2026-10-15T12:00:00%2B02:00")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	f := svc.askedFilter
	if !slices.Equal(f.Statuses, []string{"failed", "bounced"}) || f.Recipient != "Customer@Example.test" {
		t.Fatalf("filter %+v", f)
	}
	if f.CreatedAfter == nil || !f.CreatedAfter.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("created_after %v", f.CreatedAfter)
	}
	if f.CreatedBefore == nil || !f.CreatedBefore.Equal(time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("created_before %v", f.CreatedBefore)
	}
}

func TestListEmailsRejects(t *testing.T) {
	s, _ := listEmailsServer(t, principal)
	secret := "s3cr3t-value"
	for _, tc := range []struct {
		query   string
		errCode string
	}{
		{"limit=0", "invalid_limit"},
		{"limit=101", "invalid_limit"},
		{"limit=1&limit=2", "invalid_limit"},
		{"cursor=nope!", "invalid_cursor"},
		{"cursor=a&cursor=b", "invalid_cursor"},
		{"status=" + secret, "invalid_filter"},
		{"status=", "invalid_filter"},
		{"status=a&status=b&status=c&status=d&status=e&status=f&status=g&status=h&status=queued", "invalid_filter"},
		{"recipient=" + secret, "invalid_filter"},
		{"recipient=a@example.test&recipient=b@example.test", "invalid_filter"},
		{"created_after=" + secret, "invalid_filter"},
		{"created_after=2026-10-15", "invalid_filter"},
		{"created_before=2026-10-01T00:00:00Z&created_before=2026-10-02T00:00:00Z", "invalid_filter"},
		{"created_after=2026-10-02T00:00:00Z&created_before=2026-10-02T00:00:00Z", "invalid_filter"},
		{"created_after=2026-10-03T00:00:00Z&created_before=2026-10-02T00:00:00Z", "invalid_filter"},
		{secret + "=1", "invalid_filter"},
		{"template_id=" + uuid.NewString(), "invalid_filter"},
		{"limit=%zz", "invalid_filter"},
	} {
		rec, _ := getList(t, s, "/v1/emails?"+tc.query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code %d, want 400", tc.query, rec.Code)
			continue
		}
		assertErrorCode(t, rec, tc.errCode)
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("%s: response echoes the value: %s", tc.query, rec.Body.String())
		}
	}
}

func TestListEmailsRequiresReadScope(t *testing.T) {
	sendOnly := auth.Principal{ProjectID: principal.ProjectID, Scopes: []string{auth.ScopeEmailsSend}}
	s, svc := listEmailsServer(t, sendOnly)
	if rec, _ := getList(t, s, "/v1/emails"); rec.Code != http.StatusForbidden {
		t.Fatalf("without emails:read: %d, want 403", rec.Code)
	}
	if svc.askedLimit != 0 {
		t.Fatal("service called without the read scope")
	}
}

func TestListEmailsDatabaseErrors(t *testing.T) {
	for _, tc := range []struct {
		err     error
		code    int
		errCode string
	}{
		{fmt.Errorf("list: %w", &pgconn.PgError{Code: "57P03"}), http.StatusServiceUnavailable, "service_unavailable"},
		{errors.New("boom"), http.StatusInternalServerError, "internal_error"},
		{emails.ErrListBusy, http.StatusServiceUnavailable, "service_unavailable"},
	} {
		s, svc := listEmailsServer(t, principal)
		svc.err = tc.err
		rec, _ := getList(t, s, "/v1/emails")
		if rec.Code != tc.code {
			t.Fatalf("%v: code %d, want %d", tc.err, rec.Code, tc.code)
		}
		assertErrorCode(t, rec, tc.errCode)
		if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "57P03") {
			t.Fatalf("leaks the error: %s", rec.Body.String())
		}
	}
}
