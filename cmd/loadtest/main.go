//go:build load

// Command loadtest measures the send path under load: it posts emails with
// a fixed concurrency against a running API, then waits until the worker has
// finished all of them, sampling database lock waits and process memory.
// It is not part of the shipped binaries (build tag "load"); run it through
// `make load`.
//
// The API key is read from LOADTEST_API_KEY and the database URL from
// DATABASE_URL, never from flags (visible in process listings).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

type result struct {
	status  int
	latency time.Duration
	id      string
}

func run() error {
	addr := flag.String("addr", "http://127.0.0.1:18082", "API base URL")
	concurrency := flag.Int("c", 20, "concurrent clients")
	total := flag.Int("n", 2000, "requests")
	size := flag.Int("size", 2048, "text body size in bytes")
	drainTimeout := flag.Duration("drain-timeout", 10*time.Minute, "how long to wait for the worker")
	pids := flag.String("pids", "", "comma-separated api,worker PIDs for memory sampling")
	flag.Parse()
	key := os.Getenv("LOADTEST_API_KEY")
	if key == "" {
		return errors.New("LOADTEST_API_KEY is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("cannot connect to DATABASE_URL")
	}
	defer pool.Close()

	sampler := startSampler(ctx, pool, strings.Split(*pids, ","))
	body := strings.Repeat("x", *size)
	client := &http.Client{Timeout: 30 * time.Second}
	results := make([]result, *total)
	var next atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for range *concurrency {
		wg.Go(func() {
			for {
				i := int(next.Add(1)) - 1
				if i >= *total {
					return
				}
				results[i] = post(ctx, client, *addr, key, i, body)
			}
		})
	}
	wg.Wait()
	acceptDur := time.Since(start)

	var ids []string
	codes := map[int]int{}
	var lat []time.Duration
	for _, r := range results {
		codes[r.status]++
		lat = append(lat, r.latency)
		if r.status == http.StatusAccepted {
			ids = append(ids, r.id)
		}
	}
	slices.Sort(lat)

	drained, err := waitTerminal(ctx, pool, ids, *drainTimeout)
	if err != nil {
		return err
	}
	endToEnd := time.Since(start)
	s := sampler.stop()

	var statuses []string
	rows, err := pool.Query(ctx, "SELECT status, count(*) FROM emails WHERE id::text = ANY($1) GROUP BY status ORDER BY status", ids)
	if err != nil {
		return fmt.Errorf("final states: %w", err)
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return err
		}
		statuses = append(statuses, fmt.Sprintf("%s=%d", st, n))
	}
	rows.Close()
	var snoozes int64
	_ = pool.QueryRow(ctx, `SELECT coalesce(sum((metadata->>'snoozes')::int), 0) FROM river_job
		WHERE kind = 'send_email' AND args->>'email_id' = ANY($1)`, ids).Scan(&snoozes)

	fmt.Printf("parameters      concurrency=%d requests=%d body=%dB\n", *concurrency, *total, *size)
	fmt.Printf("POST status     %v\n", codes)
	fmt.Printf("POST latency    p50=%v p95=%v p99=%v max=%v\n", pct(lat, 50), pct(lat, 95), pct(lat, 99), lat[len(lat)-1])
	fmt.Printf("accept rate     %.1f req/s (%v for all requests)\n", float64(*total)/acceptDur.Seconds(), acceptDur.Round(time.Millisecond))
	fmt.Printf("queue lag       all %d accepted emails terminal %v after the first request (drain %v after the last accept)\n",
		len(ids), endToEnd.Round(time.Millisecond), drained.Round(time.Millisecond))
	fmt.Printf("dispatch rate   %.1f emails/s end to end\n", float64(len(ids))/endToEnd.Seconds())
	fmt.Printf("final states    %s; snoozes=%d\n", strings.Join(statuses, " "), snoozes)
	fmt.Printf("db lock waits   max %d sessions waiting on locks at once (%d samples)\n", s.maxLockWaits, s.samples)
	fmt.Printf("memory (RSS)    %s\n", s.memory())
	return nil
}

func post(ctx context.Context, c *http.Client, addr, key string, i int, body string) result {
	payload, _ := json.Marshal(map[string]string{
		"from": "load@example.test", "to": fmt.Sprintf("load-%d@example.test", i%100),
		"subject": fmt.Sprintf("load %d", i), "text": body,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/v1/emails", bytes.NewReader(payload))
	if err != nil {
		return result{}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", fmt.Sprintf("load-%d-%d", time.Now().UnixNano(), i))
	t0 := time.Now()
	resp, err := c.Do(req)
	lat := time.Since(t0)
	if err != nil {
		return result{status: -1, latency: lat}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &out)
	return result{status: resp.StatusCode, latency: lat, id: out.ID}
}

// waitTerminal polls until no accepted email is queued or sending and
// returns how long that took.
func waitTerminal(ctx context.Context, pool *pgxpool.Pool, ids []string, timeout time.Duration) (time.Duration, error) {
	start := time.Now()
	for {
		var open int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM emails WHERE id::text = ANY($1) AND status IN ('queued', 'sending')", ids).Scan(&open); err != nil {
			return 0, fmt.Errorf("poll: %w", err)
		}
		if open == 0 {
			return time.Since(start), nil
		}
		if time.Since(start) > timeout {
			return 0, fmt.Errorf("%d emails still open after %v", open, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := min(len(sorted)-1, (len(sorted)*p+99)/100-1)
	return sorted[max(i, 0)].Round(100 * time.Microsecond)
}

type sampler struct {
	mu           sync.Mutex
	stopCh       chan struct{}
	done         sync.WaitGroup
	samples      int
	maxLockWaits int
	maxRSS       map[string]int // pid → KiB
}

func startSampler(ctx context.Context, pool *pgxpool.Pool, pids []string) *sampler {
	s := &sampler{stopCh: make(chan struct{}), maxRSS: map[string]int{}}
	s.done.Go(func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-tick.C:
			}
			var waits int
			_ = pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'").Scan(&waits)
			s.mu.Lock()
			s.samples++
			s.maxLockWaits = max(s.maxLockWaits, waits)
			for _, pid := range pids {
				if _, err := strconv.Atoi(pid); err != nil {
					continue // only numeric PIDs reach ps
				}
				out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", pid).Output() //nolint:gosec // fixed binary, numeric argument
				if kib, err2 := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && err2 == nil {
					s.maxRSS[pid] = max(s.maxRSS[pid], kib)
				}
			}
			s.mu.Unlock()
		}
	})
	return s
}

func (s *sampler) stop() *sampler {
	close(s.stopCh)
	s.done.Wait()
	return s
}

func (s *sampler) memory() string {
	if len(s.maxRSS) == 0 {
		return "not sampled"
	}
	var parts []string
	for pid, kib := range s.maxRSS {
		parts = append(parts, fmt.Sprintf("pid %s max %.1f MiB", pid, float64(kib)/1024))
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}
