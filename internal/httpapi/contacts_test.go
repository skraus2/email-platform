package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/contacts"
)

// fakeContacts records the last input and answers with err.
type fakeContacts struct {
	err    error
	create contacts.CreateInput
	update contacts.UpdateInput
	filter contacts.ListFilter
	limit  int
	token  string
}

func (f *fakeContacts) Create(_ context.Context, _ uuid.UUID, in contacts.CreateInput) (contacts.Contact, error) {
	f.create = in
	return contacts.Contact{Attributes: json.RawMessage(`{}`)}, f.err
}

func (f *fakeContacts) Get(context.Context, uuid.UUID, uuid.UUID) (contacts.Contact, error) {
	return contacts.Contact{Attributes: json.RawMessage(`{}`)}, f.err
}

func (f *fakeContacts) Update(_ context.Context, _, _ uuid.UUID, in contacts.UpdateInput) (contacts.Contact, error) {
	f.update = in
	return contacts.Contact{Attributes: json.RawMessage(`{}`)}, f.err
}

func (f *fakeContacts) Delete(context.Context, uuid.UUID, uuid.UUID) error { return f.err }

func (f *fakeContacts) Unsubscribe(_ context.Context, _ uuid.UUID, token string) (uuid.UUID, error) {
	f.token = token
	return uuid.MustParse("01a00000-0000-7000-8000-0000000000aa"), f.err
}

func (f *fakeContacts) List(_ context.Context, _ uuid.UUID, filter contacts.ListFilter, limit int) ([]contacts.Contact, bool, error) {
	f.filter, f.limit = filter, limit
	return nil, false, f.err
}

func contactCall(t *testing.T, svc ContactService, scopes []string, method, path, body string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logs bytes.Buffer
	s := New(Deps{
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		Authenticate: func(context.Context, string) (auth.Principal, error) {
			return auth.Principal{ProjectID: uuid.New(), Scopes: scopes}, nil
		},
		Emails:   &fakeEmails{},
		Contacts: svc,
	})
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer ep_test_x")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec, logs.String()
}

var contactScopes = []string{auth.ScopeContactsRead, auth.ScopeContactsWrite}

func TestContactErrorMapping(t *testing.T) {
	id := uuid.NewString()
	for _, tc := range []struct {
		err     error
		status  int
		errCode string
	}{
		{contacts.ErrNotFound, 404, "not_found"},
		{contacts.ErrExists, 409, "contact_exists"},
		{contacts.ErrLimit, 422, "contact_limit_reached"},
		{&contacts.Error{Field: "attributes.name", Reason: "must be a string"}, 422, "validation_failed"},
		{contacts.ErrBusy, 503, "service_unavailable"},
		{fmt.Errorf("x: %w", &pgconn.PgError{Code: "57P03"}), 503, "service_unavailable"},
		{errors.New("boom secret-detail"), 500, "internal_error"},
	} {
		for _, call := range []struct{ method, path, body string }{
			{"POST", "/v1/contacts", `{"email":"a@example.test","status":"unsubscribed"}`},
			{"GET", "/v1/contacts/" + id, ""},
			{"PATCH", "/v1/contacts/" + id, `{"status":"unsubscribed"}`},
			{"DELETE", "/v1/contacts/" + id, ""},
			{"GET", "/v1/contacts", ""},
			{"POST", "/v1/unsubscribes", `{"token":"x"}`},
		} {
			rec, logs := contactCall(t, &fakeContacts{err: tc.err}, contactScopes, call.method, call.path, call.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.errCode+`"`) {
				t.Errorf("%v %s %s: %d %s", tc.err, call.method, call.path, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "secret-detail") {
				t.Errorf("internal detail leaked: %s", rec.Body)
			}
			if tc.status == 500 && !strings.Contains(logs, "failed") {
				t.Errorf("500 not logged: %s", logs)
			}
		}
	}
}

func TestContactRoutesNeedScopes(t *testing.T) {
	id := uuid.NewString()
	for _, call := range []struct{ method, path, body string }{
		{"POST", "/v1/contacts", `{"email":"a@example.test","status":"unsubscribed"}`},
		{"PATCH", "/v1/contacts/" + id, `{"status":"unsubscribed"}`},
		{"DELETE", "/v1/contacts/" + id, ""},
	} {
		if rec, _ := contactCall(t, &fakeContacts{}, []string{auth.ScopeContactsRead}, call.method, call.path, call.body); rec.Code != 403 {
			t.Errorf("%s %s with read scope: %d", call.method, call.path, rec.Code)
		}
	}
	for _, call := range []struct{ method, path string }{{"GET", "/v1/contacts/" + id}, {"GET", "/v1/contacts"}} {
		if rec, _ := contactCall(t, &fakeContacts{}, []string{auth.ScopeContactsWrite, auth.ScopeEmailsRead}, call.method, call.path, ""); rec.Code != 403 {
			t.Errorf("%s %s with write scope: %d", call.method, call.path, rec.Code)
		}
	}
	if rec, _ := contactCall(t, &fakeContacts{}, contactScopes, "PUT", "/v1/contacts/"+id, "{}"); rec.Code != 405 {
		t.Errorf("PUT: %d", rec.Code)
	}
	if rec, _ := contactCall(t, &fakeContacts{}, contactScopes, "GET", "/v1/contacts/not-a-uuid", ""); rec.Code != 404 {
		t.Errorf("malformed id: %d", rec.Code)
	}
}

func TestUpdateContactRequest(t *testing.T) {
	id := "/v1/contacts/" + uuid.NewString()
	f := &fakeContacts{}
	rec, _ := contactCall(t, f, contactScopes, "PATCH", id, `{"status":"subscribed","consent_source":"form","attributes":{"a":1}}`)
	if rec.Code != 200 || *f.update.Status != "subscribed" || *f.update.ConsentSource != "form" || string(f.update.Attributes) != `{"a":1}` {
		t.Fatalf("full patch: %d %+v", rec.Code, f.update)
	}
	f = &fakeContacts{}
	rec, _ = contactCall(t, f, contactScopes, "PATCH", id, `{"attributes":{}}`)
	if rec.Code != 200 || f.update.Status != nil || f.update.ConsentSource != nil || string(f.update.Attributes) != `{}` {
		t.Fatalf("attributes only: %d %+v", rec.Code, f.update)
	}
	for _, body := range []string{`{"status":null}`, `{"consent_source":null}`, `{"status":1}`} {
		rec, _ := contactCall(t, &fakeContacts{}, contactScopes, "PATCH", id, body)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), "validation_failed") {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{`{"Status":"x"}`, `{"status":"a","status":"b"}`, `[]`, `{"unknown":1}`} {
		rec, _ := contactCall(t, &fakeContacts{}, contactScopes, "PATCH", id, body)
		if rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
}

func TestListContactsQuery(t *testing.T) {
	f := &fakeContacts{}
	cursor := uuid.New()
	rec, _ := contactCall(t, f, contactScopes, "GET", "/v1/contacts?email=A@example.test&status=unsubscribed&limit=7&cursor="+encodeCursor(cursor), "")
	if rec.Code != 200 || f.filter.Email != "A@example.test" || f.filter.Status != "unsubscribed" || f.limit != 7 || f.filter.After != cursor {
		t.Fatalf("query: %d %+v %d", rec.Code, f.filter, f.limit)
	}
	f = &fakeContacts{}
	if rec, _ := contactCall(t, f, contactScopes, "GET", "/v1/contacts", ""); rec.Code != 200 || f.limit != 50 || f.filter != (contacts.ListFilter{}) {
		t.Fatalf("defaults: %d %+v %d", rec.Code, f.filter, f.limit)
	}
	if rec.Body.String() != "{\"data\":[],\"next_cursor\":null}\n" {
		t.Fatalf("empty page: %s", rec.Body)
	}
	for q, code := range map[string]string{
		"?other=1": "invalid_filter", "?email=x": "invalid_filter", "?email=a@example.test&email=b@example.test": "invalid_filter",
		"?status=pending": "invalid_filter", "?status=subscribed&status=unsubscribed": "invalid_filter",
		"?limit=0": "invalid_limit", "?limit=101": "invalid_limit", "?cursor=zz": "invalid_cursor",
		"?limit=1&limit=2": "invalid_limit", "?cursor=" + encodeCursor(cursor) + "&cursor=" + encodeCursor(cursor): "invalid_cursor",
	} {
		rec, _ := contactCall(t, &fakeContacts{}, contactScopes, "GET", "/v1/contacts"+q, "")
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), code) || strings.Contains(rec.Body.String(), "pending") {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
}

func TestUnsubscribeRequest(t *testing.T) {
	const tok = "TOKEN-VALUE-NEVER-ECHOED-0123456789abcdefgh"
	f := &fakeContacts{}
	rec, logs := contactCall(t, f, []string{auth.ScopeContactsWrite}, "POST", "/v1/unsubscribes", `{"token":"`+tok+`"}`)
	if rec.Code != 200 || f.token != tok ||
		rec.Body.String() != "{\"contact_id\":\"01a00000-0000-7000-8000-0000000000aa\",\"status\":\"unsubscribed\"}\n" {
		t.Fatalf("unsubscribe: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs, tok) {
		t.Fatal("token logged")
	}
	rec, logs = contactCall(t, &fakeContacts{err: contacts.ErrNotFound}, []string{auth.ScopeContactsWrite}, "POST", "/v1/unsubscribes", `{"token":"`+tok+`"}`)
	if rec.Code != 404 || strings.Contains(rec.Body.String(), tok) || strings.Contains(logs, tok) {
		t.Fatalf("unknown token: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := contactCall(t, &fakeContacts{}, []string{auth.ScopeContactsRead}, "POST", "/v1/unsubscribes", `{"token":"x"}`); rec.Code != 403 {
		t.Fatalf("read scope: %d", rec.Code)
	}
	// A missing token is an unknown one: the store decides (404).
	f = &fakeContacts{token: "sentinel"}
	if rec, _ := contactCall(t, f, []string{auth.ScopeContactsWrite}, "POST", "/v1/unsubscribes", `{}`); rec.Code != 200 || f.token != "" {
		t.Errorf("missing token: %d, store saw %q", rec.Code, f.token)
	}
	for _, body := range []string{`{"token":1}`, `{"token":"a","x":1}`} {
		f := &fakeContacts{}
		rec, _ := contactCall(t, f, []string{auth.ScopeContactsWrite}, "POST", "/v1/unsubscribes", body)
		if rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
}
