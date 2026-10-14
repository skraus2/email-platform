//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Database connections dropped under load (server restart, failover,
// administrator kill) must never surface as 500: requests that hit a broken
// connection get 503, and the API recovers without a restart.
func TestDroppedConnectionsGive503AndRecover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStack(t)
	_, token := s.project(t)
	var id string
	if code, body := s.do(t, http.MethodPost, "/v1/emails", token, sendBody); code == http.StatusAccepted {
		id = body["id"].(string)
	} else {
		t.Fatalf("setup POST = %d", code)
	}

	stop := make(chan struct{})
	var killer sync.WaitGroup
	killer.Go(func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			// A separate connection kills every other session of this database.
			_, _ = s.pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()`)
		}
	})

	// Plain requests: t.Fatal must not run outside the test goroutine.
	call := func(method, path, body string) int {
		req, err := http.NewRequestWithContext(ctx, method, s.srv.URL+path, strings.NewReader(body))
		if err != nil {
			return -1
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return -1
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	var mu sync.Mutex
	codes := map[int]int{}
	var clients sync.WaitGroup
	for range 4 {
		clients.Go(func() {
			for range 25 {
				post := call(http.MethodPost, "/v1/emails", sendBody)
				get := call(http.MethodGet, "/v1/emails/"+id, "")
				mu.Lock()
				codes[post]++
				codes[get]++
				mu.Unlock()
			}
		})
	}
	clients.Wait()
	close(stop)
	killer.Wait()

	if codes[http.StatusInternalServerError] > 0 {
		t.Fatalf("status codes under dropped connections: %v; want no 500", codes)
	}
	for code := range codes {
		switch code {
		case http.StatusOK, http.StatusAccepted, http.StatusServiceUnavailable:
		default:
			t.Fatalf("unexpected status %d (all: %v)", code, codes)
		}
	}
	if codes[http.StatusServiceUnavailable] == 0 {
		t.Fatalf("no request hit a dropped connection (codes %v); the outage was not exercised", codes)
	}

	// Recovery without restart, for sending and reading.
	deadline := time.Now().Add(5 * time.Second)
	for call(http.MethodPost, "/v1/emails", sendBody) != http.StatusAccepted || call(http.MethodGet, "/v1/emails/"+id, "") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("API did not recover after the connections were dropped")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("status codes during the outage: %v", codes)
}
