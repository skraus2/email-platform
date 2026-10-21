// Package testmailpit lets integration tests inspect what the local Mailpit
// captured, via its HTTP API.
package testmailpit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// Env vars (set by `make test-integration`, defaults match compose.yaml).
const (
	EnvAPI  = "TEST_MAILPIT_URL"
	EnvSMTP = "TEST_SMTP_ADDR"
)

// SMTPAddr returns the Mailpit SMTP address for tests.
func SMTPAddr() string {
	if v := os.Getenv(EnvSMTP); v != "" {
		return v
	}
	return "127.0.0.1:1025"
}

func apiBase() string {
	if v := os.Getenv(EnvAPI); v != "" {
		return v
	}
	return "http://127.0.0.1:8025"
}

// Message is the part of Mailpit's message view the tests compare.
type Message struct {
	ID        string // Mailpit's own id
	MessageID string
	From      struct{ Address string }
	To        []struct{ Address string }
	Subject   string
	Text      string
	HTML      string
}

// FindByMessageID waits up to timeout for messages with the given Message-ID
// (without angle brackets) and returns all of them, fully loaded.
func FindByMessageID(t testing.TB, messageID string, timeout time.Duration) []Message {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ids := search(t, messageID)
		if len(ids) > 0 || time.Now().After(deadline) {
			out := make([]Message, 0, len(ids))
			for _, id := range ids {
				var m Message
				get(t, "/api/v1/message/"+url.PathEscape(id), &m)
				out = append(out, m)
			}
			return out
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Headers returns the raw headers of a captured message (by Mailpit id).
func Headers(t testing.TB, id string) map[string][]string {
	t.Helper()
	var h map[string][]string
	get(t, "/api/v1/message/"+url.PathEscape(id)+"/headers", &h)
	return h
}

func search(t testing.TB, messageID string) []string {
	t.Helper()
	var res struct {
		Messages []struct{ ID string }
	}
	get(t, "/api/v1/search?query="+url.QueryEscape(fmt.Sprintf("message-id:%q", messageID)), &res)
	ids := make([]string, 0, len(res.Messages))
	for _, m := range res.Messages {
		ids = append(ids, m.ID)
	}
	return ids
}

func get(t testing.TB, path string, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("testmailpit: %v (is `make up` running?)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("testmailpit: GET %s: %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("testmailpit: decode %s: %v", path, err)
	}
}
