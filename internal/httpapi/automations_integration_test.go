//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/templates"
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

func TestContactEventsOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "automations:write", "events:write", "templates:write", "contacts:write")
	_, other := s.project(t, "events:write", "contacts:write")

	_, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"welcome"}`)
	tmpl := body["id"].(string)
	s.do(t, "POST", "/v1/templates/"+tmpl+"/versions", token, jsonBody(t, map[string]any{
		"subject": "Welcome", "text": "Hi {{.order}} - {{.unsubscribe_url}}",
		"variables": map[string]any{"unsubscribe_url": map[string]any{"type": "string", "required": true}, "order": map[string]any{"type": "string"}},
	}))
	code, body := s.do(t, "POST", "/v1/automations", token, jsonBody(t, map[string]any{
		"name": "Welcome", "trigger": map[string]any{"event": "user.signed_up"},
		"steps": []any{map[string]any{"type": "send", "from": "news@example.test", "template_id": tmpl, "template_version": 1,
			"unsubscribe_url": "https://app.example.test/u?t={token}"}},
	}))
	if code != 201 {
		t.Fatalf("automation: %d %v", code, body)
	}
	automation := body["id"].(string)
	_, body = s.do(t, "POST", "/v1/contacts", token, `{"email":"ann@example.test","status":"subscribed","consent_source":"signup"}`)
	contact := body["id"].(string)
	_, body = s.do(t, "POST", "/v1/contacts", other, `{"email":"bob@example.test","status":"subscribed","consent_source":"signup"}`)
	foreign := body["id"].(string)

	report := `{"name":"user.signed_up","email":"Ann@example.test","data":{"order":"A-1"}}`
	code, first := s.doWith(t, "POST", "/v1/contact-events", token, report, map[string]string{"Idempotency-Key": "signup-ann"})
	runs, _ := first["runs"].([]any)
	if code != 202 || first["contact_id"] != contact || len(runs) != 1 || runs[0].(map[string]any)["automation_id"] != automation {
		t.Fatalf("report: %d %v", code, first)
	}
	// A retried request (lost response) starts nothing new.
	code, again := s.doWith(t, "POST", "/v1/contact-events", token, report, map[string]string{"Idempotency-Key": "signup-ann"})
	if code != 202 || again["id"] != first["id"] || len(again["runs"].([]any)) != 1 || again["runs"].([]any)[0].(map[string]any)["id"] != runs[0].(map[string]any)["id"] {
		t.Fatalf("replay: %d %v", code, again)
	}
	if code, body := s.doWith(t, "POST", "/v1/contact-events", token, `{"name":"user.signed_up","email":"ann@example.test"}`,
		map[string]string{"Idempotency-Key": "signup-ann"}); code != 409 || errorCode(body) != "idempotency_key_reused" {
		t.Fatalf("reused key: %d %v", code, body)
	}
	// Contacts of other projects are unknown.
	for _, body := range []string{`{"name":"e","contact_id":"` + foreign + `"}`, `{"name":"e","email":"bob@example.test"}`} {
		if code, resp := s.do(t, "POST", "/v1/contact-events", token, body); code != 422 || errorCode(resp) != "contact_not_found" {
			t.Fatalf("foreign contact: %d %v", code, resp)
		}
	}
	// Another project's event never starts this project's automation.
	if code, body := s.do(t, "POST", "/v1/contact-events", other, `{"name":"user.signed_up","contact_id":"`+foreign+`"}`); code != 202 || len(body["runs"].([]any)) != 0 {
		t.Fatalf("other project: %d %v", code, body)
	}
}

func TestRunsOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "automations:read", "automations:write", "events:write", "templates:write", "contacts:write", "emails:read")
	_, other := s.project(t, "automations:read", "automations:write")

	_, body := s.do(t, "POST", "/v1/templates", token, `{"slug":"welcome"}`)
	tmpl := body["id"].(string)
	s.do(t, "POST", "/v1/templates/"+tmpl+"/versions", token, jsonBody(t, map[string]any{
		"subject": "Welcome", "text": "Hi - {{.unsubscribe_url}}",
		"variables": map[string]any{"unsubscribe_url": map[string]any{"type": "string", "required": true}},
	}))
	send := map[string]any{"type": "send", "from": "news@example.test", "template_id": tmpl, "template_version": 1,
		"unsubscribe_url": "https://app.example.test/u?t={token}"}
	_, body = s.do(t, "POST", "/v1/automations", token, jsonBody(t, map[string]any{
		"name": "Welcome", "trigger": map[string]any{"event": "user.signed_up"},
		"steps": []any{send, map[string]any{"type": "wait", "seconds": 3600}, send},
	}))
	automation := body["id"].(string)
	_, body = s.do(t, "POST", "/v1/contacts", token, `{"email":"ann@example.test","status":"subscribed","consent_source":"signup"}`)
	contact := body["id"].(string)
	_, body = s.do(t, "POST", "/v1/contact-events", token, `{"name":"user.signed_up","contact_id":"`+contact+`","data":{"secret":"x"}}`)
	run := body["runs"].([]any)[0].(map[string]any)["id"].(string)

	// Run the first step as the worker would.
	enq, err := jobs.NewEnqueuer(s.pool, slog.New(slog.DiscardHandler), 5)
	if err != nil {
		t.Fatal(err)
	}
	engine := automations.NewEngine(s.pool, enq, slog.New(slog.DiscardHandler), templates.NewCache(1<<20, 1<<20))
	project := uuid.Nil
	if err := s.pool.QueryRow(context.Background(), `SELECT project_id FROM automation_runs WHERE id = $1`, run).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := engine.Step(context.Background(), jobs.AutomationStepArgs{RunID: uuid.MustParse(run), ProjectID: project, Step: 0}, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatal(err)
	}

	code, body := s.do(t, "GET", "/v1/automation-runs/"+run, token, "")
	emailsOfRun, _ := body["emails"].([]any)
	if code != 200 || body["status"] != "active" || body["step"] != 2.0 || body["next_step_at"] == nil || body["reason"] != nil ||
		body["contact_id"] != contact || body["automation_id"] != automation || len(emailsOfRun) != 1 || len(body) != 11 {
		t.Fatalf("get run: %d %v", code, body)
	}
	if strings.Contains(fmt.Sprint(body), "secret") {
		t.Fatalf("run shows trigger data: %v", body)
	}
	email := emailsOfRun[0].(map[string]any)["id"].(string)
	if code, body := s.do(t, "GET", "/v1/emails/"+email, token, ""); code != 200 || body["automation_id"] != automation || body["automation_run_id"] != run {
		t.Fatalf("email: %d %v", code, body)
	}
	if code, body := s.do(t, "GET", "/v1/emails", token, ""); code != 200 || body["data"].([]any)[0].(map[string]any)["automation_run_id"] != run {
		t.Fatalf("email list: %d %v", code, body)
	}
	code, body = s.do(t, "GET", "/v1/automations/"+automation+"/runs?status=active", token, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 1 || data[0].(map[string]any)["emails"] != nil {
		t.Fatalf("list runs: %d %v", code, body)
	}
	for _, q := range []string{"?status=done", "?contact_id=x", "?x=1"} {
		if code, _ := s.do(t, "GET", "/v1/automations/"+automation+"/runs"+q, token, ""); code != 400 {
			t.Fatalf("%s: %d", q, code)
		}
	}
	// Other projects see nothing.
	for _, call := range []struct{ method, path string }{
		{"GET", "/v1/automation-runs/" + run}, {"POST", "/v1/automation-runs/" + run + "/cancel"}, {"GET", "/v1/automations/" + automation + "/runs"},
	} {
		if code, _ := s.do(t, call.method, call.path, other, ""); code != 404 {
			t.Fatalf("foreign %s %s: %d", call.method, call.path, code)
		}
	}
	code, body = s.do(t, "POST", "/v1/automation-runs/"+run+"/cancel", token, "")
	if code != 200 || body["status"] != "canceled" || body["reason"] != "canceled" || body["next_step_at"] != nil {
		t.Fatalf("cancel: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/automation-runs/"+run+"/cancel", token, ""); code != 409 || errorCode(body) != "run_not_cancelable" {
		t.Fatalf("second cancel: %d %v", code, body)
	}
	if code, body := s.do(t, "GET", "/v1/automations/"+automation, token, ""); code != 200 || body["run_counts"].(map[string]any)["canceled"] != 1.0 {
		t.Fatalf("run counts: %d %v", code, body)
	}
}
