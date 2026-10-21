package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/contacts"
)

type fakeAudiences struct {
	err   error
	ids   []uuid.UUID
	after uuid.UUID
	limit int
}

func (f *fakeAudiences) Create(context.Context, uuid.UUID, string) (contacts.Audience, error) {
	return contacts.Audience{}, f.err
}

func (f *fakeAudiences) Get(context.Context, uuid.UUID, uuid.UUID) (contacts.Audience, int64, error) {
	return contacts.Audience{}, 7, f.err
}

func (f *fakeAudiences) List(_ context.Context, _, after uuid.UUID, limit int) ([]contacts.Audience, bool, error) {
	f.after, f.limit = after, limit
	return nil, false, f.err
}

func (f *fakeAudiences) Add(_ context.Context, _, _ uuid.UUID, ids []uuid.UUID) (contacts.AddResult, error) {
	f.ids = ids
	return contacts.AddResult{Added: 1, AlreadyMembers: 2, NotFound: 3}, f.err
}

func (f *fakeAudiences) AddOne(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeAudiences) Remove(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeAudiences) Members(_ context.Context, _, _, after uuid.UUID, limit int) ([]contacts.Contact, bool, error) {
	f.after, f.limit = after, limit
	return nil, false, f.err
}

func audienceCall(t *testing.T, svc AudienceService, scopes []string, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	s := New(Deps{
		Logger: slog.New(slog.DiscardHandler),
		Authenticate: func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{ProjectID: uuid.New(), Scopes: scopes}, nil
		},
		Emails:    &fakeEmails{},
		Audiences: svc,
	})
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer ep_test_x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAudienceErrorMapping(t *testing.T) {
	id, cid := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct {
		err     error
		status  int
		errCode string
	}{
		{contacts.ErrNotFound, 404, "not_found"},
		{contacts.ErrAudienceExists, 409, "audience_exists"},
		{contacts.ErrAudienceLimit, 422, "audience_limit_reached"},
		{contacts.ErrAudienceFull, 422, "audience_full"},
		{&contacts.Error{Field: "contact_ids", Reason: "must not repeat an id"}, 422, "validation_failed"},
		{contacts.ErrBusy, 503, "service_unavailable"},
		{errors.New("boom secret-detail"), 500, "internal_error"},
	} {
		for _, call := range []struct{ method, path, body string }{
			{"POST", "/v1/audiences", `{"name":"a"}`},
			{"GET", "/v1/audiences/" + id, ""},
			{"GET", "/v1/audiences", ""},
			{"POST", "/v1/audiences/" + id + "/contacts", `{"contact_ids":["` + cid + `"]}`},
			{"GET", "/v1/audiences/" + id + "/contacts", ""},
			{"PUT", "/v1/audiences/" + id + "/contacts/" + cid, ""},
			{"DELETE", "/v1/audiences/" + id + "/contacts/" + cid, ""},
		} {
			rec := audienceCall(t, &fakeAudiences{err: tc.err}, contactScopes, call.method, call.path, call.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.errCode+`"`) || strings.Contains(rec.Body.String(), "secret-detail") {
				t.Errorf("%v %s %s: %d %s", tc.err, call.method, call.path, rec.Code, rec.Body)
			}
		}
	}
}

func TestAudienceRequests(t *testing.T) {
	id, cid := uuid.NewString(), uuid.NewString()
	f := &fakeAudiences{}
	rec := audienceCall(t, f, contactScopes, "POST", "/v1/audiences/"+id+"/contacts", `{"contact_ids":["`+cid+`"]}`)
	if rec.Code != 200 || len(f.ids) != 1 || f.ids[0].String() != cid ||
		rec.Body.String() != "{\"added\":1,\"already_members\":2,\"not_found\":3}\n" {
		t.Fatalf("bulk add: %d %s %v", rec.Code, rec.Body, f.ids)
	}
	for _, body := range []string{`{"contact_ids":["nope"]}`, `{"contact_ids":["` + strings.ToUpper(cid) + `x"]}`, `{"contact_ids":["{` + cid + `}"]}`} {
		if rec := audienceCall(t, &fakeAudiences{}, contactScopes, "POST", "/v1/audiences/"+id+"/contacts", body); rec.Code != 422 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	many := `{"contact_ids":[` + strings.Repeat(`"`+cid+`",`, 1000) + `"` + cid + `"]}`
	for _, body := range []string{`{"contact_ids":[]}`, many} {
		f := &fakeAudiences{}
		if rec := audienceCall(t, f, contactScopes, "POST", "/v1/audiences/"+id+"/contacts", body); rec.Code != 422 || f.ids != nil {
			t.Errorf("%d ids: %d (reached the store: %v)", strings.Count(body, cid), rec.Code, f.ids != nil)
		}
	}
	if rec := audienceCall(t, &fakeAudiences{}, contactScopes, "POST", "/v1/audiences/"+id+"/contacts", `{"ids":[]}`); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	rec = audienceCall(t, &fakeAudiences{}, contactScopes, "GET", "/v1/audiences/"+id, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"contact_count":7`) {
		t.Fatalf("get: %s", rec.Body)
	}
	rec = audienceCall(t, &fakeAudiences{}, contactScopes, "GET", "/v1/audiences", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "contact_count") {
		t.Fatalf("list: %s", rec.Body)
	}
	f = &fakeAudiences{}
	cursor := uuid.New()
	if rec := audienceCall(t, f, contactScopes, "GET", "/v1/audiences/"+id+"/contacts?limit=3&cursor="+encodeCursor(cursor), ""); rec.Code != 200 || f.limit != 3 || f.after != cursor {
		t.Fatalf("members paging: %d %d %v", rec.Code, f.limit, f.after)
	}
	for _, q := range []string{"?email=a@example.test", "?limit=101", "?cursor=x"} {
		if rec := audienceCall(t, &fakeAudiences{}, contactScopes, "GET", "/v1/audiences"+q, ""); rec.Code != 400 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	for _, p := range []string{"/v1/audiences/x/contacts/" + cid, "/v1/audiences/" + id + "/contacts/x"} {
		if rec := audienceCall(t, &fakeAudiences{}, contactScopes, "PUT", p, ""); rec.Code != 404 {
			t.Errorf("malformed %s: %d", p, rec.Code)
		}
	}
	// Scopes: writes need contacts:write, reads contacts:read.
	if rec := audienceCall(t, &fakeAudiences{}, []string{auth.ScopeContactsRead}, "PUT", "/v1/audiences/"+id+"/contacts/"+cid, ""); rec.Code != 403 {
		t.Errorf("PUT with read scope: %d", rec.Code)
	}
	if rec := audienceCall(t, &fakeAudiences{}, []string{auth.ScopeContactsWrite}, "GET", "/v1/audiences/"+id+"/contacts", ""); rec.Code != 403 {
		t.Errorf("GET with write scope: %d", rec.Code)
	}
}
