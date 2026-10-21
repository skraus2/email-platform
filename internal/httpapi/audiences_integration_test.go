//go:build integration

package httpapi_test

import (
	"fmt"
	"testing"
)

func TestAudienceLifecycleOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "contacts:read", "contacts:write")
	_, other := s.project(t, "contacts:read", "contacts:write")

	var ids []string
	for i := range 3 {
		_, body := s.do(t, "POST", "/v1/contacts", token, fmt.Sprintf(`{"email":"m%d@example.test","status":"subscribed","consent_source":"x"}`, i))
		ids = append(ids, body["id"].(string))
	}
	_, body := s.do(t, "POST", "/v1/contacts", other, `{"email":"o@example.test","status":"unsubscribed"}`)
	foreign := body["id"].(string)

	code, body := s.do(t, "POST", "/v1/audiences", token, `{"name":"Newsletter"}`)
	if code != 201 || body["name"] != "Newsletter" || len(body) != 3 {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	if code, body := s.do(t, "POST", "/v1/audiences", token, `{"name":"newsletter"}`); code != 409 || errorCode(body) != "audience_exists" {
		t.Fatalf("duplicate: %d %v", code, body)
	}

	if code, _ := s.do(t, "PUT", "/v1/audiences/"+id+"/contacts/"+ids[0], token, ""); code != 204 {
		t.Fatalf("add one: %d", code)
	}
	code, body = s.do(t, "POST", "/v1/audiences/"+id+"/contacts", token,
		fmt.Sprintf(`{"contact_ids":["%s","%s","%s","%s"]}`, ids[0], ids[1], ids[2], foreign))
	if code != 200 || body["added"] != 2.0 || body["already_members"] != 1.0 || body["not_found"] != 1.0 {
		t.Fatalf("bulk add: %d %v", code, body)
	}
	if code, body := s.do(t, "GET", "/v1/audiences/"+id, token, ""); code != 200 || body["contact_count"] != 3.0 {
		t.Fatalf("get: %d %v", code, body)
	}
	code, body = s.do(t, "GET", "/v1/audiences/"+id+"/contacts?limit=2", token, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 2 || body["next_cursor"] == nil {
		t.Fatalf("members: %d %v", code, body)
	}
	if code, _ := s.do(t, "DELETE", "/v1/audiences/"+id+"/contacts/"+ids[0], token, ""); code != 204 {
		t.Fatalf("remove: %d", code)
	}

	// Another project sees and changes nothing.
	for _, call := range []struct{ method, path, body string }{
		{"GET", "/v1/audiences/" + id, ""},
		{"GET", "/v1/audiences/" + id + "/contacts", ""},
		{"PUT", "/v1/audiences/" + id + "/contacts/" + foreign, ""},
		{"DELETE", "/v1/audiences/" + id + "/contacts/" + ids[1], ""},
		{"POST", "/v1/audiences/" + id + "/contacts", `{"contact_ids":["` + foreign + `"]}`},
	} {
		if code, body := s.do(t, call.method, call.path, other, call.body); code != 404 || errorCode(body) != "not_found" {
			t.Errorf("%s %s by another project: %d %v", call.method, call.path, code, body)
		}
	}
	if code, _ := s.do(t, "PUT", "/v1/audiences/"+id+"/contacts/"+foreign, token, ""); code != 404 {
		t.Fatalf("foreign contact: %d", code)
	}
	code, body = s.do(t, "GET", "/v1/audiences", other, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 0 {
		t.Fatalf("foreign list: %d %v", code, body)
	}
	if code, body := s.do(t, "GET", "/v1/audiences/"+id, token, ""); code != 200 || body["contact_count"] != 2.0 {
		t.Fatalf("after foreign attempts: %d %v", code, body)
	}
}
