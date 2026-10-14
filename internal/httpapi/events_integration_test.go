//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestListEventsOverHTTP(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStack(t)
	_, token := s.project(t)
	_, otherToken := s.project(t)
	_, sendOnly := s.project(t, "emails:send")

	code, body := s.do(t, http.MethodPost, "/v1/emails", token, sendBody)
	if code != http.StatusAccepted {
		t.Fatalf("POST = %d", code)
	}
	id := body["id"].(string)
	// Three stored events (a failure history is enough to page through).
	for range 3 {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO email_events (project_id, email_id, origin, type, occurred_at, details)
			SELECT project_id, id, 'platform', 'failed', now(), '{"error_code":"expired"}' FROM emails WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	path := "/v1/emails/" + id + "/events"

	code, page := s.do(t, http.MethodGet, path+"?limit=2", token, "")
	data, _ := page["data"].([]any)
	cursor, _ := page["next_cursor"].(string)
	if code != http.StatusOK || len(data) != 2 || cursor == "" {
		t.Fatalf("first page = %d %v", code, page)
	}
	first := data[0].(map[string]any)
	if first["type"] != "failed" || first["origin"] != "platform" || first["provider"] != nil ||
		first["details"].(map[string]any)["error_code"] != "expired" {
		t.Fatalf("event = %v", first)
	}
	code, page = s.do(t, http.MethodGet, path+"?limit=2&cursor="+url.QueryEscape(cursor), token, "")
	data, _ = page["data"].([]any)
	if code != http.StatusOK || len(data) != 1 || page["next_cursor"] != nil {
		t.Fatalf("second page = %d %v", code, page)
	}

	// Another project's key sees the same 404 as for an unknown id, and a
	// cursor never widens the scope.
	code, foreign := s.do(t, http.MethodGet, path+"?cursor="+url.QueryEscape(cursor), otherToken, "")
	code2, unknown := s.do(t, http.MethodGet, "/v1/emails/01a13601-4b3e-72cd-9dcd-4d9159938763/events", token, "")
	if code != http.StatusNotFound || code2 != http.StatusNotFound || foreign["error"].(map[string]any)["code"] != unknown["error"].(map[string]any)["code"] {
		t.Fatalf("foreign = %d %v, unknown = %d %v", code, foreign, code2, unknown)
	}
	if code, _ := s.do(t, http.MethodGet, path, sendOnly, ""); code != http.StatusForbidden {
		t.Fatalf("send-only key = %d, want 403", code)
	}
}
