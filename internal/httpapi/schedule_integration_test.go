//go:build integration

package httpapi_test

import (
	"testing"
	"time"
)

func TestScheduleAndCancelOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t, "emails:send", "emails:read")
	_, other := s.project(t, "emails:send", "emails:read")

	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	code, body := s.do(t, "POST", "/v1/emails", token,
		`{"from":"app@example.test","to":"user@example.test","subject":"Later","text":"x","scheduled_at":"`+at.Format(time.RFC3339)+`"}`)
	if code != 202 {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)
	code, body = s.do(t, "GET", "/v1/emails/"+id, token, "")
	if code != 200 || body["status"] != "queued" || body["scheduled_at"] != at.Format(time.RFC3339) {
		t.Fatalf("get: %d %v", code, body)
	}

	if code, body := s.do(t, "POST", "/v1/emails/"+id+"/cancel", other, ""); code != 404 || errorCode(body) != "not_found" {
		t.Fatalf("foreign cancel: %d %v", code, body)
	}
	code, body = s.do(t, "POST", "/v1/emails/"+id+"/cancel", token, "")
	if code != 200 || body["status"] != "canceled" || body["finished_at"] == nil || body["error_code"] != nil {
		t.Fatalf("cancel: %d %v", code, body)
	}
	if code, body := s.do(t, "POST", "/v1/emails/"+id+"/cancel", token, ""); code != 409 || errorCode(body) != "email_not_cancelable" {
		t.Fatalf("second cancel: %d %v", code, body)
	}
	code, body = s.do(t, "GET", "/v1/emails?status=canceled", token, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 1 {
		t.Fatalf("filter: %d %v", code, body)
	}
	code, body = s.do(t, "GET", "/v1/emails/"+id+"/events", token, "")
	if data, _ := body["data"].([]any); code != 200 || len(data) != 1 || data[0].(map[string]any)["type"] != "canceled" {
		t.Fatalf("events: %d %v", code, body)
	}

	far := time.Now().Add(31 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if code, body := s.do(t, "POST", "/v1/emails", token,
		`{"from":"app@example.test","to":"user@example.test","subject":"x","text":"x","scheduled_at":"`+far+`"}`); code != 422 || errorCode(body) != "validation_failed" {
		t.Fatalf("beyond horizon: %d %v", code, body)
	}
	// Without scheduled_at the field is null.
	_, body = s.do(t, "POST", "/v1/emails", token, `{"from":"app@example.test","to":"user@example.test","subject":"Now","text":"x"}`)
	if _, got := s.do(t, "GET", "/v1/emails/"+body["id"].(string), token, ""); got["scheduled_at"] != nil {
		t.Fatalf("immediate: %v", got)
	}
}
