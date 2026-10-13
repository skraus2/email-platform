//go:build integration

package emails_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
)

// The queue stores every job error it sees. Those must be the sanitized
// texts, never provider replies (which can contain recipient addresses).
var testRetry = jobs.RetryPolicy{Base: time.Second, Max: 10 * time.Second}

func TestQueueStoresOnlySanitizedErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	logger := slog.New(slog.DiscardHandler)
	enq, err := jobs.NewEnqueuer(pool, logger, testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	e, err := emails.NewService(pool, enq, logger, unlimited).Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}

	failed := make(chan struct{}, 10)
	p := &fakeProvider{send: func(context.Context, delivery.Message) (delivery.Result, error) {
		failed <- struct{}{}
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassRejected, Code: 451}
	}}
	d := emails.NewDispatcher(pool, p, logger, 5*time.Second, 168*time.Hour)
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: logger, Concurrency: 1, JobTimeout: 10 * time.Second, Retry: testRetry,
		Handlers: jobs.Handlers{SendEmail: d.SendEmail, Cleanup: d.Cleanup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })

	select {
	case <-failed:
	case <-time.After(15 * time.Second):
		t.Fatal("job never ran")
	}

	var errorsText string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, "SELECT coalesce(errors::text, '') FROM river_job WHERE args->>'email_id' = $1", e.ID.String()).Scan(&errorsText)
		if errorsText != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(errorsText, "delivery failed: rejected (smtp 451)") {
		t.Fatalf("queue error = %q, want the sanitized error", errorsText)
	}
	for _, leak := range []string{"customer@example.test", "noreply@example.test", "Hello"} {
		if strings.Contains(errorsText, leak) {
			t.Fatalf("queue error contains %q: %s", leak, errorsText)
		}
	}
	if s := state(t, pool, e.ID); s.status != "queued" || !s.hasContent {
		t.Fatalf("after a temporary failure the email must wait for retry: %+v", s)
	}
}

// AC12: a worker that died mid-send leaves job "running" and email
// "sending". The rescuer retries it, the email ends captured, and the
// possible duplicate is logged rather than hidden.
func TestInterruptedDispatchIsRescuedAndFlagged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	quiet := slog.New(slog.DiscardHandler)
	enq, _ := jobs.NewEnqueuer(pool, quiet, testMaxAttempts)
	e, err := emails.NewService(pool, enq, quiet, unlimited).Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the crash: job running since an hour, email mid-send.
	if _, err := pool.Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1,
		attempted_at = now() - interval '1 hour', attempted_by = '{crashed-worker}'
		WHERE args->>'email_id' = $1`, e.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE emails SET status = 'sending', attempts = 1 WHERE id = $1", e.ID); err != nil {
		t.Fatal(err)
	}

	var logs safeBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	p := &fakeProvider{send: captured}
	d := emails.NewDispatcher(pool, p, logger, 5*time.Second, 168*time.Hour)
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: quiet, Concurrency: 1, JobTimeout: 500 * time.Millisecond, RescueStuckJobsAfter: time.Second, Retry: testRetry,
		Handlers: jobs.Handlers{SendEmail: d.SendEmail, Cleanup: d.Cleanup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && state(t, pool, e.ID).status != "captured" {
		time.Sleep(100 * time.Millisecond)
	}
	if s := state(t, pool, e.ID); s.status != "captured" || s.hasContent {
		t.Fatalf("rescued email state = %+v, want captured with content deleted", s)
	}
	if !strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Fatal("rescued attempt was not flagged as a possible duplicate")
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
