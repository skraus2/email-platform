package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/templates"
)

type fakeBroadcasts struct {
	err    error
	in     broadcasts.CreateInput
	at     *time.Time
	status string
	called bool
}

func (f *fakeBroadcasts) Create(_ context.Context, _ uuid.UUID, in broadcasts.CreateInput) (broadcasts.Broadcast, error) {
	f.in, f.called = in, true
	return broadcasts.Broadcast{Status: "draft"}, f.err
}

func (f *fakeBroadcasts) Get(context.Context, uuid.UUID, uuid.UUID) (broadcasts.Broadcast, error) {
	return broadcasts.Broadcast{Counts: &broadcasts.Counts{ByStatus: map[string]int{"queued": 2}}}, f.err
}

func (f *fakeBroadcasts) List(_ context.Context, _ uuid.UUID, status string, _ uuid.UUID, _ int) ([]broadcasts.Broadcast, bool, error) {
	f.status = status
	return nil, false, f.err
}

func (f *fakeBroadcasts) Schedule(_ context.Context, _, _ uuid.UUID, at *time.Time) (broadcasts.Broadcast, error) {
	f.at, f.called = at, true
	return broadcasts.Broadcast{Status: "scheduled"}, f.err
}

func (f *fakeBroadcasts) Cancel(context.Context, uuid.UUID, uuid.UUID) (broadcasts.Broadcast, error) {
	return broadcasts.Broadcast{Status: "canceled"}, f.err
}

var broadcastScopes = []string{auth.ScopeBroadcastsRead, auth.ScopeBroadcastsWrite}

func broadcastCall(t *testing.T, svc BroadcastService, scopes []string, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := New(Deps{
		Logger: slog.New(slog.DiscardHandler),
		Authenticate: func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{ProjectID: uuid.New(), Scopes: scopes}, nil
		},
		Emails:     &fakeEmails{},
		Broadcasts: svc,
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

func validBroadcastBody(version string) string {
	return `{"audience_id":"` + uuid.NewString() + `","from":"n@example.test","template_id":"` + uuid.NewString() +
		`","template_version":` + version + `,"unsubscribe_url":"https://a.test/u?t={token}","variables":{"plan":"pro"}}`
}

func TestBroadcastErrorMapping(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{broadcasts.ErrNotFound, 404, "not_found"},
		{broadcasts.ErrAudienceNotFound, 422, "audience_not_found"},
		{broadcasts.ErrTemplateNotFound, 422, "template_not_found"},
		{broadcasts.ErrMissingUnsubscribe, 422, "template_missing_unsubscribe"},
		{broadcasts.ErrLimit, 422, "broadcast_limit_reached"},
		{broadcasts.ErrRateLimited, 429, "rate_limited"},
		{emails.ErrSenderNotVerified, 422, "sender_not_verified"},
		{broadcasts.ErrNotDraft, 409, "broadcast_not_draft"},
		{broadcasts.ErrNotCancelable, 409, "broadcast_not_cancelable"},
		{&broadcasts.Error{Field: "unsubscribe_url", Reason: "bad"}, 422, "validation_failed"},
		{&templates.Error{Code: templates.CodeVariablesInvalid, Field: "variables.plan", Reason: "bad"}, 422, "variables_invalid"},
		{broadcasts.ErrBusy, 503, "service_unavailable"},
		{errors.New("boom secret-detail"), 500, "internal_error"},
	} {
		for _, call := range []struct{ method, path, body string }{
			{"POST", "/v1/broadcasts", validBroadcastBody(`"published"`)},
			{"GET", "/v1/broadcasts/" + id, ""},
			{"GET", "/v1/broadcasts", ""},
			{"POST", "/v1/broadcasts/" + id + "/schedule", ""},
			{"POST", "/v1/broadcasts/" + id + "/cancel", ""},
		} {
			rec := broadcastCall(t, &fakeBroadcasts{err: tc.err}, broadcastScopes, call.method, call.path, "application/json", call.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) || strings.Contains(rec.Body.String(), "secret-detail") {
				t.Errorf("%v %s %s: %d %s", tc.err, call.method, call.path, rec.Code, rec.Body)
			}
		}
	}
}

func TestBroadcastRequests(t *testing.T) {
	id := "/v1/broadcasts/" + uuid.NewString()
	f := &fakeBroadcasts{}
	if rec := broadcastCall(t, f, broadcastScopes, "POST", "/v1/broadcasts", "application/json", validBroadcastBody(`3`)); rec.Code != 201 ||
		f.in.TemplateVersion != 3 || f.in.UnsubscribeURL != "https://a.test/u?t={token}" || string(f.in.Variables) != `{"plan":"pro"}` {
		t.Fatalf("create: %d %+v", rec.Code, f.in)
	}
	f = &fakeBroadcasts{}
	if rec := broadcastCall(t, f, broadcastScopes, "POST", "/v1/broadcasts", "application/json", validBroadcastBody(`"published"`)); rec.Code != 201 || f.in.TemplateVersion != 0 {
		t.Fatalf("published: %d", rec.Code)
	}
	for _, v := range []string{`0`, `-1`, `"latest"`, `1.5`, `null`} {
		f := &fakeBroadcasts{}
		if rec := broadcastCall(t, f, broadcastScopes, "POST", "/v1/broadcasts", "application/json", validBroadcastBody(v)); rec.Code != 422 || f.called {
			t.Errorf("version %s: %d", v, rec.Code)
		}
	}
	for _, body := range []string{
		`{"audience_id":"x","from":"n@example.test","template_id":"` + uuid.NewString() + `","template_version":1,"unsubscribe_url":"u"}`,
		`{"audience_id":"` + uuid.NewString() + `","from":"n@example.test","template_id":"nope","template_version":1,"unsubscribe_url":"u"}`,
	} {
		f := &fakeBroadcasts{}
		if rec := broadcastCall(t, f, broadcastScopes, "POST", "/v1/broadcasts", "application/json", body); rec.Code != 422 || f.called {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if rec := broadcastCall(t, &fakeBroadcasts{}, broadcastScopes, "POST", "/v1/broadcasts", "application/json", `{"x":1}`); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}

	// Schedule: no body = now; JSON body with a time; malformed time 422.
	f = &fakeBroadcasts{}
	if rec := broadcastCall(t, f, broadcastScopes, "POST", id+"/schedule", "", ""); rec.Code != 200 || f.at != nil {
		t.Fatalf("schedule now: %d", rec.Code)
	}
	f = &fakeBroadcasts{}
	if rec := broadcastCall(t, f, broadcastScopes, "POST", id+"/schedule", "application/json", `{"scheduled_at":"2026-11-01T09:00:00Z"}`); rec.Code != 200 ||
		f.at == nil || !f.at.Equal(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("schedule later: %d %v", rec.Code, f.at)
	}
	if rec := broadcastCall(t, &fakeBroadcasts{}, broadcastScopes, "POST", id+"/schedule", "application/json", `{"scheduled_at":"soon"}`); rec.Code != 422 {
		t.Errorf("bad time: %d", rec.Code)
	}
	if rec := broadcastCall(t, &fakeBroadcasts{}, broadcastScopes, "POST", id+"/schedule", "text/plain", `{"scheduled_at":"2026-11-01T09:00:00Z"}`); rec.Code != 415 {
		t.Errorf("content type: %d", rec.Code)
	}

	rec := broadcastCall(t, &fakeBroadcasts{}, broadcastScopes, "GET", id, "", "")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["counts"] == nil || got["variables"] != nil {
		t.Fatalf("get: %s", rec.Body)
	}
	rec = broadcastCall(t, &fakeBroadcasts{}, broadcastScopes, "GET", "/v1/broadcasts", "", "")
	if strings.Contains(rec.Body.String(), "counts") {
		t.Fatalf("list has counts: %s", rec.Body)
	}
	f = &fakeBroadcasts{}
	if rec := broadcastCall(t, f, broadcastScopes, "GET", "/v1/broadcasts?status=sending", "", ""); rec.Code != 200 || f.status != "sending" {
		t.Fatalf("status filter: %d %q", rec.Code, f.status)
	}
	for _, q := range []string{"?status=sent", "?status=draft&status=sending", "?x=1", "?limit=0"} {
		if rec := broadcastCall(t, &fakeBroadcasts{}, broadcastScopes, "GET", "/v1/broadcasts"+q, "", ""); rec.Code != 400 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}

	// Scopes.
	if rec := broadcastCall(t, &fakeBroadcasts{}, []string{auth.ScopeBroadcastsRead}, "POST", id+"/cancel", "", ""); rec.Code != 403 {
		t.Errorf("cancel with read scope: %d", rec.Code)
	}
	if rec := broadcastCall(t, &fakeBroadcasts{}, []string{auth.ScopeBroadcastsWrite}, "GET", id, "", ""); rec.Code != 403 {
		t.Errorf("get with write scope: %d", rec.Code)
	}
}
