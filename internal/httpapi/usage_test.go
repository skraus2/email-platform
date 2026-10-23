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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/usage"
)

type fakeUsage struct {
	days               []usage.Day
	err                error
	project            uuid.UUID
	askedFrom, askedTo time.Time
	askedProject       uuid.UUID
}

func (f *fakeUsage) Days(_ context.Context, projectID uuid.UUID, from, to time.Time) ([]usage.Day, error) {
	f.askedProject, f.askedFrom, f.askedTo = projectID, from, to
	if f.err != nil {
		return nil, f.err
	}
	var out []usage.Day
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		day := usage.Day{Date: d}
		for _, s := range f.days {
			if s.Date.Equal(d) && projectID == f.project {
				day = s
			}
		}
		out = append(out, day)
	}
	return out, nil
}

func usageServer(svc *fakeUsage, p auth.Principal, authErr error) (*Server, *bytes.Buffer) {
	var logs bytes.Buffer
	return New(Deps{
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		Authenticate: func(context.Context, string) (auth.Principal, error) {
			return p, authErr
		},
		Usage:       svc,
		UsageLimits: UsageLimits{SendsPerMinute: 600, ContactEventsPerMinute: 1200},
	}), &logs
}

func getUsage(t *testing.T, s *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/usage"+query, nil)
	req.Header.Set("Authorization", "Bearer ep_test_x")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestParseUsageRange(t *testing.T) {
	now := time.Date(2026, 10, 23, 23, 30, 0, 0, time.FixedZone("CEST", 2*3600)) // 21:30 UTC
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	for _, tc := range []struct {
		query    string
		from, to string
	}{
		{"", "2026-09-24", "2026-10-23"},
		{"to=2026-10-01", "2026-09-02", "2026-10-01"},
		{"from=2026-10-20", "2026-10-20", "2026-10-23"},
		{"from=2026-10-23&to=2026-10-23", "2026-10-23", "2026-10-23"},
		{"from=2026-07-23&to=2026-10-23", "2026-07-23", "2026-10-23"}, // 93 days
		{"from=2026-10-24&to=2026-10-30", "2026-10-24", "2026-10-30"}, // future days are allowed
	} {
		q, _ := url.ParseQuery(tc.query)
		from, to, msg := parseUsageRange(q, now)
		if msg != "" || !from.Equal(d(tc.from)) || !to.Equal(d(tc.to)) {
			t.Errorf("%q: %v..%v %q, want %s..%s", tc.query, from, to, msg, tc.from, tc.to)
		}
	}
}

func TestUsageValidation(t *testing.T) {
	t.Parallel()
	p := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsRead}}
	svc := &fakeUsage{}
	s, _ := usageServer(svc, p, nil)
	for _, tc := range []struct{ query, msg string }{
		{"?limit=5", "unknown query parameter"},
		{"?from=2026-10-01&from=2026-10-02", "from must be a single date (YYYY-MM-DD)"},
		{"?from=yesterday", "from must be a single date (YYYY-MM-DD)"},
		{"?to=2026-13-01", "to must be a single date (YYYY-MM-DD)"},
		{"?to=2026-10-1", "to must be a single date (YYYY-MM-DD)"},
		{"?from=2026-10-02&to=2026-10-01", "from must not be after to"},
		{"?from=2026-07-22&to=2026-10-23", "from and to may be at most 93 days apart (both included)"},
		{"?%zz", "the query string is malformed"},
	} {
		rec := getUsage(t, s, tc.query)
		want := fmt.Sprintf(`{"error":{"code":"invalid_filter","message":%q}}`, tc.msg)
		if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != want {
			t.Errorf("%s: %d %s, want 400 %s", tc.query, rec.Code, rec.Body.String(), want)
		}
		if strings.Contains(rec.Body.String(), "yesterday") || strings.Contains(rec.Body.String(), "2026-13") {
			t.Errorf("%s: error message echoes the value", tc.query)
		}
	}
	if svc.askedProject != uuid.Nil {
		t.Error("an invalid request reached the usage store")
	}
}

func TestUsageResponse(t *testing.T) {
	t.Parallel()
	p := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsRead}}
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	svc := &fakeUsage{project: p.ProjectID, days: []usage.Day{
		{Date: d("2026-10-02"), Counts: usage.Counts{Accepted: 5, Sent: 4, Failed: 1, Delivered: 3, Bounced: 1}},
		{Date: d("2026-10-03"), Counts: usage.Counts{Accepted: 2, Sent: 2, Canceled: 1, Complained: 1, Bounced: 1}},
	}}
	s, _ := usageServer(svc, p, nil)
	rec := getUsage(t, s, "?from=2026-10-01&to=2026-10-03")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if svc.askedProject != p.ProjectID {
		t.Errorf("store asked for project %v, want the key's %v", svc.askedProject, p.ProjectID)
	}
	want := `{"from":"2026-10-01","to":"2026-10-03","data":[` +
		`{"date":"2026-10-01","accepted":0,"sent":0,"failed":0,"canceled":0,"delivered":0,"bounced":0,"complained":0},` +
		`{"date":"2026-10-02","accepted":5,"sent":4,"failed":1,"canceled":0,"delivered":3,"bounced":1,"complained":0},` +
		`{"date":"2026-10-03","accepted":2,"sent":2,"failed":0,"canceled":1,"delivered":0,"bounced":1,"complained":1}],` +
		`"totals":{"accepted":7,"sent":6,"failed":1,"canceled":1,"delivered":3,"bounced":2,"complained":1},` +
		`"limits":{"sends_per_minute":600,"contact_events_per_minute":1200}}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body usageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Data) != 3 {
		t.Errorf("decode: %v, %d days", err, len(body.Data))
	}
}

func TestUsageAuth(t *testing.T) {
	t.Parallel()
	sendOnly := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsSend}}
	s, _ := usageServer(&fakeUsage{}, sendOnly, nil)
	if rec := getUsage(t, s, ""); rec.Code != http.StatusForbidden {
		t.Errorf("key without emails:read: %d, want 403", rec.Code)
	}
	s, _ = usageServer(&fakeUsage{}, auth.Principal{}, auth.ErrUnauthorized)
	if rec := getUsage(t, s, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid key: %d, want 401", rec.Code)
	}
	p := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsRead}}
	s, _ = usageServer(&fakeUsage{}, p, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/usage", nil)
	req.Header.Set("Authorization", "Bearer ep_test_x")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d Allow=%q, want 405 GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestUsageDatabaseUnavailable(t *testing.T) {
	t.Parallel()
	p := auth.Principal{ProjectID: uuid.New(), Scopes: []string{auth.ScopeEmailsRead}}
	for _, tc := range []struct {
		err  error
		code int
		want string
	}{
		{fmt.Errorf("list usage: %w", &pgconn.PgError{Code: "57P03"}), http.StatusServiceUnavailable, "service_unavailable"},
		{errors.New("boom: secret detail"), http.StatusInternalServerError, "internal_error"},
	} {
		s, logs := usageServer(&fakeUsage{err: tc.err}, p, nil)
		rec := getUsage(t, s, "")
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), `"code":"`+tc.want+`"`) {
			t.Errorf("%v: %d %s", tc.err, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "secret detail") || strings.Contains(rec.Body.String(), "57P03") {
			t.Errorf("internal error text leaked: %s", rec.Body.String())
		}
		if tc.code == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
			t.Error("503 without Retry-After")
		}
		if !strings.Contains(logs.String(), p.ProjectID.String()) {
			t.Error("failure log lacks project_id")
		}
	}
}
