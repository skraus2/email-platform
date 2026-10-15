//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// listIDs pages through GET /v1/emails?query with the given page size and
// returns all ids in order.
func (s *stack) listIDs(t *testing.T, token, query string, limit int) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for range 1000 {
		q := fmt.Sprintf("/v1/emails?limit=%d", limit)
		if query != "" {
			q += "&" + query
		}
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		code, body := s.do(t, "GET", q, token, "")
		if code != 200 {
			t.Fatalf("GET %s: %d %v", q, code, body)
		}
		for _, item := range body["data"].([]any) {
			ids = append(ids, item.(map[string]any)["id"].(string))
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			return ids
		}
		cursor = next
	}
	t.Fatal("pagination did not end")
	return nil
}

// insertEmails adds n emails of the project directly (faster than the API).
func (s *stack) insertEmails(t *testing.T, projectID uuid.UUID, n int, recipient string) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `
		INSERT INTO emails (project_id, sender, recipient, subject)
		SELECT $1, 'noreply@example.test', $2, 'Hi' FROM generate_series(1, $3)
		RETURNING id::text`, projectID, recipient, n)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestListEmailsIsolation(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	a, tokenA := s.project(t)
	b, tokenB := s.project(t)
	ownA := s.insertEmails(t, a, 3, "alice@example.test")
	ownB := s.insertEmails(t, b, 4, "bob@example.test")

	if got := s.listIDs(t, tokenA, "", 2); len(got) != 3 || strings.Join(got, ",") != strings.Join(reverse(ownA), ",") {
		t.Fatalf("project A sees %v, want %v newest first", got, reverse(ownA))
	}
	if got := s.listIDs(t, tokenB, "", 100); len(got) != 4 {
		t.Fatalf("project B sees %d emails, want 4", len(got))
	}
	// Filtering for the other project's recipient finds nothing.
	if got := s.listIDs(t, tokenA, "recipient=bob@example.test", 100); len(got) != 0 {
		t.Fatalf("A found B's recipient: %v", got)
	}
	// A cursor taken from B's list only positions A's scan.
	code, body := s.do(t, "GET", "/v1/emails?limit=1", tokenB, "")
	if code != 200 {
		t.Fatal(code)
	}
	foreignCursor := body["next_cursor"].(string)
	code, body = s.do(t, "GET", "/v1/emails?limit=100&cursor="+url.QueryEscape(foreignCursor), tokenA, "")
	if code != 200 {
		t.Fatal(code, body)
	}
	for _, item := range body["data"].([]any) {
		id := item.(map[string]any)["id"].(string)
		for _, other := range ownB {
			if id == other {
				t.Fatalf("A's page with B's cursor contains B's email %s", id)
			}
		}
	}
	// Without the read scope the list is refused.
	_, sendOnly := s.project(t, "emails:send")
	if code, _ := s.do(t, "GET", "/v1/emails", sendOnly, ""); code != 403 {
		t.Fatalf("send-only key: %d, want 403", code)
	}
}

func reverse(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

func TestListEmailsFilters(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	p, token := s.project(t)
	ctx := context.Background()
	failed := s.insertEmails(t, p, 2, "Customer@Example.test")
	bounced := s.insertEmails(t, p, 1, "other@example.test")
	queued := s.insertEmails(t, p, 1, "customer@example.test")
	if _, err := s.pool.Exec(ctx, `
		UPDATE emails SET status = 'failed', error_code = 'provider_rejected', finished_at = now()
		WHERE id = ANY($1::uuid[])`, failed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE emails SET status = 'bounced', provider = 'ses', finished_at = now()
		WHERE id = ANY($1::uuid[])`, bounced); err != nil {
		t.Fatal(err)
	}
	// Spread creation times (all before the ids were generated): failed[0]
	// on 1 October, the rest on 10 October.
	if _, err := s.pool.Exec(ctx, `UPDATE emails SET created_at = '2026-10-01T10:00:00Z' WHERE id = $1`, failed[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE emails SET created_at = '2026-10-10T10:00:00Z' WHERE id <> $1 AND project_id = $2`, failed[0], p); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"status=failed", reverse(failed)},
		{"status=failed&status=bounced", append(reverse(bounced), reverse(failed)...)},
		{"status=delivered", nil},
		{"recipient=customer@example.test", append(append([]string{}, queued...), reverse(failed)...)},
		{"recipient=CUSTOMER@EXAMPLE.TEST&status=queued", queued},
		{"created_after=2026-10-05T00:00:00Z", append(append(append([]string{}, queued...), bounced...), failed[1])},
		{"created_before=2026-10-10T10:00:00Z", failed[:1]},
		{"created_after=2026-10-01T10:00:00Z&created_before=2026-10-01T10:00:00.000001Z", failed[:1]},
		{"created_after=2026-10-01T10:00:00.000001Z&created_before=2026-10-10T10:00:00Z", nil},
	} {
		got := s.listIDs(t, token, tc.query, 2)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestListEmailsPagingConcurrentInserts(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	p, token := s.project(t)
	existing := s.insertEmails(t, p, 250, "bulk@example.test")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	t.Cleanup(func() { close(stop); wg.Wait() })
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				// t.Fatal must not run outside the test goroutine.
				if _, err := s.pool.Exec(context.Background(), `
					INSERT INTO emails (project_id, sender, recipient, subject)
					SELECT $1, 'noreply@example.test', 'late@example.test', 'Hi' FROM generate_series(1, 3)`, p); err != nil {
					t.Error(err)
					return
				}
			}
		}
	})
	got := s.listIDs(t, token, "recipient=bulk@example.test", 37)
	all := s.listIDs(t, token, "", 37)

	if len(got) != len(existing) {
		t.Fatalf("listed %d of %d pre-existing emails", len(got), len(existing))
	}
	seen := map[string]int{}
	for _, id := range all {
		seen[id]++
	}
	for _, id := range existing {
		if seen[id] != 1 {
			t.Fatalf("pre-existing email %s listed %d times in the unfiltered paging", id, seen[id])
		}
	}
}
