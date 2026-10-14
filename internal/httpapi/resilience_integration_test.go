//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
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

	var mu sync.Mutex
	codes := map[int]int{}
	var clients sync.WaitGroup
	for range 4 {
		clients.Go(func() {
			for range 25 {
				post, _ := s.do(t, http.MethodPost, "/v1/emails", token, sendBody)
				get, _ := s.do(t, http.MethodGet, "/v1/emails/"+id, token, "")
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
		t.Logf("no request hit a dropped connection (codes %v); the test did not exercise the outage", codes)
	}

	// Recovery without restart.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if code, _ := s.do(t, http.MethodPost, "/v1/emails", token, sendBody); code == http.StatusAccepted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("API did not recover after the connections were dropped")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("status codes during the outage: %v", codes)
}
