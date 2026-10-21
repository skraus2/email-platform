//go:build integration

package httpapi_test

import (
	"testing"
)

func TestBroadcastLifecycleOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "broadcasts:read", "broadcasts:write", "contacts:write", "templates:write", "emails:read")
	_, other := s.project(t, "broadcasts:read", "broadcasts:write")

	_, body := s.do(t, "POST", "/v1/audiences", token, `{"name":"news"}`)
	audience := body["id"].(string)
	_, body = s.do(t, "POST", "/v1/templates", token, `{"slug":"news"}`)
	tmpl := body["id"].(string)
	code, body := s.do(t, "POST", "/v1/templates/"+tmpl+"/versions", token, jsonBody(t, map[string]any{
		"subject": "News", "text": "Hi {{.name}} - {{.unsubscribe_url}}",
		"html":      `<p>Hi {{.name}}</p><a href="{{.unsubscribe_url}}">Unsubscribe</a>`,
		"variables": map[string]any{"unsubscribe_url": map[string]any{"type": "string", "required": true}, "name": map[string]any{"type": "string"}},
	}))
	if code != 201 {
		t.Fatalf("publish: %d %v", code, body)
	}

	create := jsonBody(t, map[string]any{
		"name": "October", "audience_id": audience, "from": "news@example.test", "template_id": tmpl,
		"template_version": "published", "unsubscribe_url": "https://app.example.test/u?t={token}",
	})
	code, body = s.do(t, "POST", "/v1/broadcasts", token, create)
	if code != 201 || body["status"] != "draft" || body["template_version"] != 1.0 || body["scheduled_at"] != nil || len(body) != 12 {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	if code, body := s.do(t, "POST", "/v1/broadcasts", other, create); code != 422 || errorCode(body) != "audience_not_found" {
		t.Fatalf("other project with foreign audience: %d %v", code, body)
	}

	code, body = s.do(t, "GET", "/v1/broadcasts/"+id, token, "")
	counts, _ := body["counts"].(map[string]any)
	if byStatus, _ := counts["by_status"].(map[string]any); code != 200 || counts["recipients"] != 0.0 || len(byStatus) != 9 {
		t.Fatalf("get: %d %v", code, body)
	}
	if code, _ := s.do(t, "GET", "/v1/broadcasts/"+id, other, ""); code != 404 {
		t.Fatalf("foreign get: %d", code)
	}
	if code, _ := s.do(t, "POST", "/v1/broadcasts/"+id+"/schedule", other, ""); code != 404 {
		t.Fatalf("foreign schedule: %d", code)
	}
	if code, _ := s.do(t, "POST", "/v1/broadcasts/"+id+"/cancel", other, ""); code != 404 {
		t.Fatalf("foreign cancel: %d", code)
	}

	code, body = s.do(t, "POST", "/v1/broadcasts/"+id+"/schedule", token, `{"scheduled_at":"2099-01-01T00:00:00Z"}`)
	if code != 422 || errorCode(body) != "validation_failed" {
		t.Fatalf("beyond horizon: %d %v", code, body)
	}
	code, body = s.do(t, "POST", "/v1/broadcasts/"+id+"/schedule", token, "")
	if code != 200 || body["status"] != "scheduled" || body["scheduled_at"] == nil {
		t.Fatalf("schedule: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/broadcasts/"+id+"/schedule", token, ""); code != 409 || errorCode(body) != "broadcast_not_draft" {
		t.Fatalf("second schedule: %d %v", code, body)
	}
	code, body = s.do(t, "GET", "/v1/broadcasts?status=scheduled", token, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/broadcasts/"+id+"/cancel", token, ""); code != 200 || body["status"] != "canceled" {
		t.Fatalf("cancel: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/broadcasts/"+id+"/cancel", token, ""); code != 409 || errorCode(body) != "broadcast_not_cancelable" {
		t.Fatalf("second cancel: %d %v", code, body)
	}
	if code, body := s.do(t, "GET", "/v1/emails?broadcast_id="+id, token, ""); code != 200 || len(body["data"].([]any)) != 0 {
		t.Fatalf("email filter: %d %v", code, body)
	}
}
