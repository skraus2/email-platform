//go:build integration

package emails_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
)

// runWorker starts a real worker with fast retries for the given provider.
func runWorker(t *testing.T, pool *pgxpool.Pool, p delivery.Provider, logs *safeBuffer) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	d := emails.NewDispatcher(pool, p, logger, 2*time.Second, 168*time.Hour)
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: logger, Concurrency: 4, JobTimeout: 10 * time.Second,
		Retry:    jobs.RetryPolicy{Base: 100 * time.Millisecond, Max: 200 * time.Millisecond},
		Handlers: jobs.Handlers{SendEmail: d.SendEmail, Cleanup: d.Cleanup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })
}

func enqueue(t *testing.T, pool *pgxpool.Pool, projectID uuid.UUID, n int) []uuid.UUID {
	t.Helper()
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), 20)
	if err != nil {
		t.Fatal(err)
	}
	svc := emails.NewService(pool, enq, slog.New(slog.DiscardHandler), unlimited)
	ids := make([]uuid.UUID, 0, n)
	for range n {
		e, err := svc.Create(context.Background(), projectID, input)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	return ids
}

func waitCaptured(t *testing.T, pool *pgxpool.Pool, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for count(t, pool, "SELECT count(*) FROM emails WHERE status = 'captured'") < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d emails captured; states: %d failed", count(t, pool, "SELECT count(*) FROM emails WHERE status = 'captured'"),
				n, count(t, pool, "SELECT count(*) FROM emails WHERE status = 'failed'"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// flakyProvider fails the first n calls per message with class, then accepts.
type flakyProvider struct {
	mu    sync.Mutex
	n     int
	class string
	seen  map[uuid.UUID]int
}

func (f *flakyProvider) Send(ctx context.Context, m delivery.Message) (delivery.Result, error) {
	f.mu.Lock()
	f.seen[m.ID]++
	call := f.seen[m.ID]
	f.mu.Unlock()
	if call <= f.n {
		return delivery.Result{}, &delivery.Error{Class: f.class}
	}
	return delivery.Result{Provider: "fake", ProviderID: "<" + m.ID.String() + ">", Status: delivery.StatusCaptured}, nil
}

func TestProviderOutageRecovers(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	ids := enqueue(t, pool, projectID, 5)
	p := &flakyProvider{n: 3, class: delivery.ClassConnect, seen: map[uuid.UUID]int{}}
	logs := &safeBuffer{}
	runWorker(t, pool, p, logs)
	waitCaptured(t, pool, len(ids), 30*time.Second)

	if n := count(t, pool, "SELECT count(*) FROM emails WHERE attempts <> 4 OR error_code IS NOT NULL OR last_error IS NOT NULL"); n != 0 {
		t.Errorf("%d emails without exactly 4 attempts and a cleared error", n)
	}
	if strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Error("connect failures (nothing sent) logged as possible duplicates")
	}
}

// Accepted-then-timed-out (the documented duplicate risk): the email stays
// "sending" with provider_timeout while retrying, every retry warns about a
// possible duplicate, and it ends captured.
func TestProviderTimeoutRetriesWarnAndRecover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	ids := enqueue(t, pool, projectID, 1)
	gate := make(chan struct{})
	p := &timeoutThenAccept{fails: 2, gate: gate, failed: make(chan struct{})}
	logs := &safeBuffer{}
	runWorker(t, pool, p, logs)

	// After the first timeout, before the retry is let through, observe the
	// intermediate state.
	select {
	case <-p.failed:
	case <-time.After(15 * time.Second):
		close(gate)
		t.Fatal("first attempt never reached the provider")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		var code *string
		if err := pool.QueryRow(ctx, "SELECT status, error_code FROM emails WHERE id = $1", ids[0]).Scan(&status, &code); err != nil {
			t.Fatal(err)
		}
		if status == "sending" && code != nil && *code == emails.CodeProviderTimeout {
			break
		}
		if time.Now().After(deadline) {
			close(gate)
			t.Fatalf("while retrying: status %s code %v, want sending/provider_timeout", status, code)
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(gate)
	waitCaptured(t, pool, 1, 30*time.Second)

	if n := strings.Count(logs.String(), "possible_duplicate_send"); n != 2 {
		t.Errorf("possible_duplicate_send logged %d times, want 2 (once per retry)", n)
	}
	if n := count(t, pool, "SELECT count(*) FROM emails WHERE attempts = 3 AND error_code IS NULL"); n != 1 {
		t.Error("want 3 attempts and a cleared error code")
	}
}

type timeoutThenAccept struct {
	mu     sync.Mutex
	calls  int
	fails  int
	gate   chan struct{}
	failed chan struct{}
}

func (p *timeoutThenAccept) Send(ctx context.Context, m delivery.Message) (delivery.Result, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 2 {
		select { // hold the retry until the test has seen the state
		case <-p.gate:
		case <-ctx.Done():
			return delivery.Result{}, &delivery.Error{Class: delivery.ClassTimeout}
		}
	}
	if call <= p.fails {
		if call == 1 {
			close(p.failed)
		}
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassTimeout}
	}
	return delivery.Result{Provider: "fake", ProviderID: "<" + m.ID.String() + ">", Status: delivery.StatusCaptured}, nil
}

// Dropped worker database connections (server restart) delay dispatch but
// lose nothing: every queued email ends captured.
func TestWorkerSurvivesDroppedConnections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	ids := enqueue(t, pool, projectID, 20)
	p := &flakyProvider{seen: map[uuid.UUID]int{}}
	logs := &safeBuffer{}

	// The worker starts on a healthy database; the connections drop while
	// it runs.
	runWorker(t, pool, p, logs)
	killer, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		for killer.Err() == nil {
			_, _ = pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()`)
			time.Sleep(30 * time.Millisecond)
		}
	})
	time.Sleep(time.Second)
	cancel()
	wg.Wait()
	waitCaptured(t, pool, len(ids), 60*time.Second)

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		if p.seen[id] < 1 {
			t.Errorf("email %s never reached the provider", id)
		}
	}
}
