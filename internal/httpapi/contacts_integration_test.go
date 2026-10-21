//go:build integration

package httpapi_test

import (
	"context"
	"strings"
	"testing"
)

func TestContactLifecycleOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "contacts:read", "contacts:write")

	code, body := s.do(t, "POST", "/v1/contacts", token,
		`{"email":"Ada@example.test","status":"subscribed","consent_source":"signup-form","attributes":{"name":"Ada <3","n":2.50}}`)
	if code != 201 || body["email"] != "Ada@example.test" || body["status"] != "subscribed" || body["consent_source"] != "signup-form" {
		t.Fatalf("create: %d %v", code, body)
	}
	attrs, _ := body["attributes"].(map[string]any)
	if attrs["name"] != "Ada <3" || len(attrs) != 2 {
		t.Fatalf("attributes: %v", body["attributes"])
	}
	for _, f := range []string{"created_at", "updated_at", "status_changed_at"} {
		assertTime(t, body, f)
	}
	if _, ok := body["unsubscribe_token"]; ok || len(body) != 8 {
		t.Fatalf("unexpected fields: %v", body)
	}
	id := body["id"].(string)

	if code, body := s.do(t, "POST", "/v1/contacts", token, `{"email":"ada@EXAMPLE.test","status":"unsubscribed"}`); code != 409 || errorCode(body) != "contact_exists" {
		t.Fatalf("duplicate: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/contacts", token, `{"email":"b@example.test","status":"subscribed"}`); code != 422 || errorCode(body) != "validation_failed" {
		t.Fatalf("no consent source: %d %v", code, body)
	}
	code, raw := s.raw(t, "POST", "/v1/contacts", token, "application/json", `{"email":"b@example.test","status":"unsubscribed","attributes":{"secret":"`+strings.Repeat("v", 1001)+`"}}`)
	if code != 422 || strings.Contains(raw, "vvvv") || !strings.Contains(raw, "attributes.secret") {
		t.Fatalf("long attribute: %d %s", code, raw)
	}

	if code, body := s.do(t, "GET", "/v1/contacts/"+id, token, ""); code != 200 || body["id"] != id {
		t.Fatalf("get: %d %v", code, body)
	}
	code, body = s.do(t, "PATCH", "/v1/contacts/"+id, token, `{"status":"unsubscribed"}`)
	if code != 200 || body["status"] != "unsubscribed" || body["consent_source"] != "signup-form" {
		t.Fatalf("unsubscribe: %d %v", code, body)
	}
	if code, body := s.do(t, "PATCH", "/v1/contacts/"+id, token, `{"status":"subscribed"}`); code != 422 || errorCode(body) != "validation_failed" {
		t.Fatalf("resubscribe without source: %d %v", code, body)
	}

	code, body = s.do(t, "GET", "/v1/contacts?email=ADA@example.test", token, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 1 || body["next_cursor"] != nil {
		t.Fatalf("lookup: %d %v", code, body)
	}

	if code, _ := s.do(t, "DELETE", "/v1/contacts/"+id, token, ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := s.do(t, "GET", "/v1/contacts/"+id, token, ""); code != 404 {
		t.Fatalf("get after delete: %d", code)
	}
}

func TestContactIsolationOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, tokenA := s.project(t, "contacts:read", "contacts:write")
	_, tokenB := s.project(t, "contacts:read", "contacts:write")
	_, sendOnly := s.project(t, "emails:send", "emails:read")

	_, body := s.do(t, "POST", "/v1/contacts", tokenA, `{"email":"a@example.test","status":"subscribed","consent_source":"x"}`)
	id := body["id"].(string)

	_, unknown := s.do(t, "GET", "/v1/contacts/01a00000-0000-7000-8000-000000000000", tokenB, "")
	for _, call := range []struct{ method, path, body string }{
		{"GET", "/v1/contacts/" + id, ""},
		{"PATCH", "/v1/contacts/" + id, `{"status":"unsubscribed"}`},
		{"DELETE", "/v1/contacts/" + id, ""},
	} {
		code, body := s.do(t, call.method, call.path, tokenB, call.body)
		if code != 404 || errorCode(body) != errorCode(unknown) {
			t.Errorf("%s by another project: %d %v", call.method, code, body)
		}
	}
	code, body := s.do(t, "GET", "/v1/contacts?email=a@example.test", tokenB, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 0 {
		t.Fatalf("foreign lookup: %d %v", code, body)
	}
	if code, _ := s.do(t, "GET", "/v1/contacts/"+id, sendOnly, ""); code != 403 {
		t.Fatalf("missing scope: %d", code)
	}
	if code, body := s.do(t, "GET", "/v1/contacts/"+id, tokenA, ""); code != 200 || body["status"] != "subscribed" {
		t.Fatalf("contact changed by another project: %d %v", code, body)
	}
	var n int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM contacts").Scan(&n); err != nil || n != 1 {
		t.Fatalf("contacts: %d %v", n, err)
	}
}

func TestUnsubscribeOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "contacts:read", "contacts:write", "emails:send")
	_, other := s.project(t, "contacts:write")
	_, body := s.do(t, "POST", "/v1/contacts", token, `{"email":"reader@example.test","status":"subscribed","consent_source":"form"}`)
	id := body["id"].(string)
	var tok string
	if err := s.pool.QueryRow(context.Background(), "SELECT unsubscribe_token FROM contacts WHERE id = $1", id).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	report := `{"token":"` + tok + `"}`

	_, unknown := s.do(t, "POST", "/v1/unsubscribes", other, `{"token":"`+strings.Repeat("A", 43)+`"}`)
	code, foreign := s.do(t, "POST", "/v1/unsubscribes", other, report)
	if code != 404 || errorCode(foreign) != "not_found" || foreign["error"].(map[string]any)["message"] != unknown["error"].(map[string]any)["message"] {
		t.Fatalf("foreign project: %d %v vs %v", code, foreign, unknown)
	}
	for range 2 {
		code, body := s.do(t, "POST", "/v1/unsubscribes", token, report)
		if code != 200 || body["contact_id"] != id || body["status"] != "unsubscribed" || len(body) != 2 {
			t.Fatalf("report: %d %v", code, body)
		}
	}
	if _, body := s.do(t, "GET", "/v1/contacts/"+id, token, ""); body["status"] != "unsubscribed" {
		t.Fatalf("contact: %v", body)
	}
	// Unsubscribe stops broadcasts only: a transactional send still goes out.
	code, body = s.do(t, "POST", "/v1/emails", token, `{"from":"app@example.test","to":"reader@example.test","subject":"Password reset","text":"x"}`)
	if code != 202 {
		t.Fatalf("transactional send to an unsubscribed contact: %d %v", code, body)
	}
}
