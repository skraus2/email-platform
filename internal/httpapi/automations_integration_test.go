//go:build integration

package httpapi_test

import (
	"testing"
)

func TestAutomationLifecycleOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "automations:read", "automations:write", "templates:write")
	_, other := s.project(t, "automations:read", "automations:write")

	_, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"welcome"}`)
	tmpl := body["id"].(string)
	code, body := s.do(t, "POST", "/v1/templates/"+tmpl+"/versions", token, jsonBody(t, map[string]any{
		"subject": "Welcome", "text": "Hi {{.name}} - {{.unsubscribe_url}}",
		"html":      `<p>Hi {{.name}}</p><a href="{{.unsubscribe_url}}">Unsubscribe</a>`,
		"variables": map[string]any{"unsubscribe_url": map[string]any{"type": "string", "required": true}, "name": map[string]any{"type": "string"}},
	}))
	if code != 201 {
		t.Fatalf("publish: %d %v", code, body)
	}
	send := map[string]any{
		"type": "send", "from": "news@example.test", "template_id": tmpl, "template_version": "published",
		"variables": map[string]any{"name": "friend"}, "unsubscribe_url": "https://app.example.test/u?t={token}",
	}
	create := jsonBody(t, map[string]any{
		"name": "Welcome", "trigger": map[string]any{"event": "user.signed_up"},
		"steps": []any{send, map[string]any{"type": "wait", "seconds": 86400},
			map[string]any{"type": "condition", "event": "order.completed", "op": "not_occurred"}, send},
	})
	code, body = s.do(t, "POST", "/v1/automations", token, create)
	if code != 201 || body["status"] != "active" || body["allow_reentry"] != false || len(body) != 8 {
		t.Fatalf("create: %d %v", code, body)
	}
	steps := body["steps"].([]any)
	if first := steps[0].(map[string]any); len(steps) != 4 || first["template_version"] != 1.0 || first["variables"].(map[string]any)["name"] != "friend" {
		t.Fatalf("steps: %v", steps)
	}
	id := body["id"].(string)
	// The other project cannot use this template, nor see the automation.
	if code, body := s.do(t, "POST", "/v1/automations", other, create); code != 422 || errorCode(body) != "template_not_found" {
		t.Fatalf("foreign template: %d %v", code, body)
	}
	for _, call := range []struct{ method, path, body string }{
		{"GET", "/v1/automations/" + id, ""},
		{"PATCH", "/v1/automations/" + id, `{"status":"paused"}`},
	} {
		if code, _ := s.do(t, call.method, call.path, other, call.body); code != 404 {
			t.Fatalf("foreign %s: %d", call.method, code)
		}
	}
	if code, body := s.do(t, "GET", "/v1/automations", other, ""); code != 200 || len(body["data"].([]any)) != 0 {
		t.Fatalf("foreign list: %d %v", code, body)
	}

	code, body = s.do(t, "GET", "/v1/automations/"+id, token, "")
	if counts, _ := body["run_counts"].(map[string]any); code != 200 || len(counts) != 5 {
		t.Fatalf("get: %d %v", code, body)
	}
	if code, body := s.do(t, "PATCH", "/v1/automations/"+id, token, `{"status":"paused","name":"Welcome v1"}`); code != 200 || body["status"] != "paused" || body["name"] != "Welcome v1" {
		t.Fatalf("pause: %d %v", code, body)
	}
	if code, body := s.do(t, "GET", "/v1/automations?status=paused", token, ""); code != 200 || len(body["data"].([]any)) != 1 {
		t.Fatalf("list paused: %d %v", code, body)
	}
	if code, body := s.do(t, "PATCH", "/v1/automations/"+id, token, `{"status":"archived"}`); code != 200 || body["status"] != "archived" {
		t.Fatalf("archive: %d %v", code, body)
	}
	if code, body := s.do(t, "PATCH", "/v1/automations/"+id, token, `{"status":"active"}`); code != 409 || errorCode(body) != "automation_archived" {
		t.Fatalf("reactivate archived: %d %v", code, body)
	}
	bad := jsonBody(t, map[string]any{"name": "x", "trigger": map[string]any{"event": "e"}, "steps": []any{map[string]any{"type": "wait", "seconds": 60}}})
	if code, body := s.do(t, "POST", "/v1/automations", token, bad); code != 422 || errorCode(body) != "validation_failed" {
		t.Fatalf("no send step: %d %v", code, body)
	}
}
