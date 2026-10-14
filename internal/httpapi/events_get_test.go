package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
)

type eventEmails struct {
	fakeEmails
	emailID      uuid.UUID
	projectID    uuid.UUID
	all          []events.Event
	askedAfter   uuid.UUID
	askedLimit   int
	askedProject uuid.UUID
}

func (e *eventEmails) Events(_ context.Context, projectID, id, after uuid.UUID, limit int) ([]events.Event, bool, error) {
	e.askedAfter, e.askedLimit, e.askedProject = after, limit, projectID
	if id != e.emailID || projectID != e.projectID {
		return nil, false, emails.ErrNotFound
	}
	start := 0
	for i, ev := range e.all {
		if ev.ID == after {
			start = i + 1
		}
	}
	rest := e.all[start:]
	if len(rest) > limit {
		return rest[:limit], true, nil
	}
	return rest, false, nil
}

func listEvents(t *testing.T, s *Server, path string) (*httptest.ResponseRecorder, eventListResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer ep_test_x")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var body eventListResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return rec, body
}

func eventServer(t *testing.T, p auth.Principal) (*Server, *eventEmails) {
	t.Helper()
	svc := &eventEmails{emailID: uuid.New(), projectID: p.ProjectID}
	at := time.Date(2026, 10, 14, 8, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	ses := "ses"
	for i, typ := range []string{"submitted", "delivered", "bounced"} {
		svc.all = append(svc.all, events.Event{
			ID: uuid.Must(uuid.NewV7()), Type: typ, Origin: map[bool]string{true: "platform", false: "ses"}[i == 0],
			Provider: &ses, OccurredAt: at, ReceivedAt: at.Add(time.Second), Details: map[string]string{},
		})
	}
	s, _ := emailServer(t, &svc.fakeEmails, p)
	s.emails = svc
	return s, svc
}

func TestListEventsPaginates(t *testing.T) {
	s, svc := eventServer(t, principal)
	base := "/v1/emails/" + svc.emailID.String() + "/events"

	rec, page := listEvents(t, s, base+"?limit=2")
	if rec.Code != http.StatusOK || len(page.Data) != 2 || page.NextCursor == nil {
		t.Fatalf("first page = %d %+v", rec.Code, page)
	}
	if page.Data[0].Type != "submitted" || page.Data[0].Origin != "platform" || page.Data[0].OccurredAt.Location() != time.UTC {
		t.Fatalf("first event = %+v", page.Data[0])
	}
	if svc.askedProject != principal.ProjectID || svc.askedLimit != 2 {
		t.Fatalf("asked project %s limit %d", svc.askedProject, svc.askedLimit)
	}
	rec, next := listEvents(t, s, base+"?limit=2&cursor="+*page.NextCursor)
	if rec.Code != http.StatusOK || len(next.Data) != 1 || next.Data[0].Type != "bounced" || next.NextCursor != nil {
		t.Fatalf("second page = %d %+v", rec.Code, next)
	}
	if svc.askedAfter != svc.all[1].ID {
		t.Fatalf("cursor decoded to %s, want %s", svc.askedAfter, svc.all[1].ID)
	}
	def, _ := listEvents(t, s, base)
	if def.Code != http.StatusOK || svc.askedLimit != defaultEventPage {
		t.Fatalf("default limit = %d (code %d)", svc.askedLimit, def.Code)
	}
	if !strings.Contains(def.Body.String(), `"details":{}`) {
		t.Fatalf("details not an object: %s", def.Body.String())
	}
}

func TestListEventsRejects(t *testing.T) {
	s, svc := eventServer(t, principal)
	base := "/v1/emails/" + svc.emailID.String() + "/events"
	for path, want := range map[string]struct {
		code    int
		errCode string
	}{
		base + "?limit=0":                            {400, "invalid_limit"},
		base + "?limit=101":                          {400, "invalid_limit"},
		base + "?limit=abc":                          {400, "invalid_limit"},
		base + "?limit=1&limit=2":                    {400, "invalid_limit"},
		base + "?cursor=nope!":                       {400, "invalid_cursor"},
		base + "?limit=%zz":                          {400, "invalid_limit"},
		base + "?cursor=" + strings.Repeat("A", 30):  {400, "invalid_cursor"},
		"/v1/emails/not-a-uuid/events":               {404, "not_found"},
		"/v1/emails/" + uuid.NewString() + "/events": {404, "not_found"},
	} {
		rec, _ := listEvents(t, s, path)
		if rec.Code != want.code {
			t.Errorf("%s: code %d, want %d", path, rec.Code, want.code)
			continue
		}
		assertErrorCode(t, rec, want.errCode)
	}

	// The read scope is required.
	sendOnly := auth.Principal{ProjectID: principal.ProjectID, Scopes: []string{auth.ScopeEmailsSend}}
	s2, svc2 := eventServer(t, sendOnly)
	if rec, _ := listEvents(t, s2, "/v1/emails/"+svc2.emailID.String()+"/events"); rec.Code != http.StatusForbidden {
		t.Fatalf("without emails:read: %d, want 403", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, base, nil)
	req.Header.Set("Authorization", "Bearer ep_test_x")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}

type failingEvents struct {
	fakeEmails
	err error
}

func (f *failingEvents) Events(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int) ([]events.Event, bool, error) {
	return nil, false, f.err
}

func TestListEventsDatabaseErrors(t *testing.T) {
	for _, tc := range []struct {
		err     error
		code    int
		errCode string
	}{
		{fmt.Errorf("list: %w", &pgconn.PgError{Code: "57P03"}), http.StatusServiceUnavailable, "service_unavailable"},
		{errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	} {
		svc := &failingEvents{err: tc.err}
		s, logs := emailServer(t, &svc.fakeEmails, principal)
		s.emails = svc
		rec, _ := listEvents(t, s, "/v1/emails/"+uuid.NewString()+"/events")
		if rec.Code != tc.code {
			t.Fatalf("%v: code %d, want %d", tc.err, rec.Code, tc.code)
		}
		assertErrorCode(t, rec, tc.errCode)
		if tc.code == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
			t.Error("503 without Retry-After")
		}
		if strings.Contains(rec.Body.String(), "boom") || strings.Contains(rec.Body.String(), "57P03") {
			t.Errorf("internal error text in the response: %s", rec.Body.String())
		}
		_ = logs
	}
}
