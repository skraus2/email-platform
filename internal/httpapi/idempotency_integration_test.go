//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// postKeyed sends a POST /v1/emails with an Idempotency-Key and returns the
// status, the decoded body and the Idempotent-Replayed header.
func (s *stack) postKeyed(t *testing.T, token, key, body string) (int, map[string]any, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.srv.URL+"/v1/emails", strings.NewReader(body))
	if err != nil {
		t.Error(err)
		return 0, nil, ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return 0, nil, ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, resp.Header.Get("Idempotent-Replayed")
}

func TestPostEmailIdempotencyOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t)

	code, first, replayed := s.postKeyed(t, token, "order-42", sendBody)
	if code != http.StatusAccepted || replayed != "" {
		t.Fatalf("first = %d %v replayed=%q", code, first, replayed)
	}
	// Same payload, different key order and whitespace: still a replay.
	reordered := "{\n  \"text\": \"Hello\",\n  \"subject\": \"Hi\",\n  \"to\": \"customer@example.test\",\n  \"from\": \"noreply@example.test\"\n}"
	code, again, replayed := s.postKeyed(t, token, "order-42", reordered)
	if code != http.StatusAccepted || replayed != "true" || again["id"] != first["id"] || again["status"] != "queued" {
		t.Fatalf("replay = %d %v replayed=%q, want 202 with id %v", code, again, replayed, first["id"])
	}
	// Same key, different payload.
	code, conflict, _ := s.postKeyed(t, token, "order-42", strings.Replace(sendBody, `"Hi"`, `"Other"`, 1))
	if code != http.StatusConflict {
		t.Fatalf("conflict = %d %v", code, conflict)
	}
	if e, _ := conflict["error"].(map[string]any); e["code"] != "idempotency_key_reused" {
		t.Fatalf("conflict body = %v", conflict)
	}
	var n int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM emails").Scan(&n)
	if n != 1 {
		t.Fatalf("emails = %d, want 1", n)
	}
}

func TestPostEmailConcurrentSameKeyOverHTTP(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, token := s.project(t)

	const n = 20
	var wg sync.WaitGroup
	ids := make([]any, n)
	codes := make([]int, n)
	replays := make([]string, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			var body map[string]any
			codes[i], body, replays[i] = s.postKeyed(t, token, "parallel", sendBody)
			ids[i] = body["id"]
		})
	}
	close(start)
	wg.Wait()
	fresh := 0
	for i := range n {
		if codes[i] != http.StatusAccepted || ids[i] != ids[0] {
			t.Fatalf("request %d: %d id=%v, want 202 id=%v", i, codes[i], ids[i], ids[0])
		}
		switch replays[i] {
		case "":
			fresh++
		case "true":
		default:
			t.Fatalf("request %d: Idempotent-Replayed = %q", i, replays[i])
		}
	}
	if fresh != 1 {
		t.Errorf("%d responses without Idempotent-Replayed, want exactly 1", fresh)
	}
	var emails, contents, jobs int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM emails").Scan(&emails)
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM email_contents").Scan(&contents)
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM river_job WHERE kind = 'send_email'").Scan(&jobs)
	if emails != 1 || contents != 1 || jobs != 1 {
		t.Fatalf("emails = %d, contents = %d, jobs = %d, want 1 each", emails, contents, jobs)
	}
}
