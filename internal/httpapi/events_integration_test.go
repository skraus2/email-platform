//go:build integration

package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
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
	if _, err := s.pool.Exec(ctx, "UPDATE emails SET status = 'submitted', provider = 'ses', provider_id = 'msg-1', finished_at = now() WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	// Stored in this order; event times deliberately run backwards, so only
	// the receive order gives the expected sequence.
	var want []string
	for i, ev := range []struct{ origin, typ, eventID, occurred, details string }{
		{"platform", "submitted", "", "now()", `{}`},
		{"ses", "send", "send:msg-1", "now() - interval '1 hour'", `{}`},
		{"ses", "delivered", "delivery:msg-1", "now() - interval '2 hours'", `{"recipient":"customer@example.test"}`},
	} {
		var eventID any
		if ev.eventID != "" {
			eventID = ev.eventID
		}
		provider := "ses"
		var got string
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO email_events (project_id, email_id, origin, type, provider, provider_event_id, provider_message_id, occurred_at, details)
			SELECT project_id, id, $2, $3, $4, $5, CASE WHEN $5::text IS NOT NULL THEN 'msg-1' END, `+ev.occurred+`, $6::jsonb
			FROM emails WHERE id = $1 RETURNING id`, id, ev.origin, ev.typ, provider, eventID, ev.details).Scan(&got); err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		want = append(want, got)
	}
	path := "/v1/emails/" + id + "/events"

	var ids []string
	code, page := s.do(t, http.MethodGet, path+"?limit=2", token, "")
	data, _ := page["data"].([]any)
	cursor, _ := page["next_cursor"].(string)
	if code != http.StatusOK || len(data) != 2 || cursor == "" {
		t.Fatalf("first page = %d %v", code, page)
	}
	for _, d := range data {
		ids = append(ids, d.(map[string]any)["id"].(string))
	}
	code, page = s.do(t, http.MethodGet, path+"?limit=2&cursor="+url.QueryEscape(cursor), token, "")
	data, _ = page["data"].([]any)
	if code != http.StatusOK || len(data) != 1 || page["next_cursor"] != nil {
		t.Fatalf("second page = %d %v", code, page)
	}
	ids = append(ids, data[0].(map[string]any)["id"].(string))
	if !slices.Equal(ids, want) {
		t.Fatalf("ids across pages = %v, want receive order %v", ids, want)
	}

	// A full last page has no next cursor.
	code, page = s.do(t, http.MethodGet, path+"?limit=3", token, "")
	if data, _ = page["data"].([]any); code != http.StatusOK || len(data) != 3 || page["next_cursor"] != nil {
		t.Fatalf("exact page = %d %v", code, page)
	}
	// Each event has exactly the contract's fields; nothing else leaks.
	keys := []string{"details", "id", "occurred_at", "origin", "provider", "received_at", "type"}
	for _, d := range data {
		ev := d.(map[string]any)
		var got []string
		for k := range ev {
			got = append(got, k)
		}
		slices.Sort(got)
		if !slices.Equal(got, keys) {
			t.Fatalf("event keys = %v, want %v", got, keys)
		}
	}
	raw := rawGet(t, s, path+"?limit=3", token)
	for _, secret := range []string{"customer@example.test", "msg-1", "delivery:msg-1", "send:msg-1"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("response contains %q: %s", secret, raw)
		}
	}

	// Another project's key (even with a valid cursor), an unknown id and a
	// malformed id all get the identical 404.
	foreign := rawGet(t, s, path+"?cursor="+url.QueryEscape(cursor), otherToken)
	unknown := rawGet(t, s, "/v1/emails/01a13601-4b3e-72cd-9dcd-4d9159938763/events", token)
	malformed := rawGet(t, s, "/v1/emails/not-a-uuid/events", token)
	if foreign != unknown || unknown != malformed || !strings.Contains(unknown, "not_found") {
		t.Fatalf("404 bodies differ:\nforeign   %s\nunknown   %s\nmalformed %s", foreign, unknown, malformed)
	}
	if code, _ := s.do(t, http.MethodGet, path, sendOnly, ""); code != http.StatusForbidden {
		t.Fatalf("send-only key = %d, want 403", code)
	}
}

func rawGet(t *testing.T, s *stack, path, token string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, s.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
