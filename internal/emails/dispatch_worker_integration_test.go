//go:build integration

package emails_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
)

// The queue stores every job error it sees. Those must be the sanitized
// texts, never provider replies (which can contain recipient addresses).
func TestQueueStoresOnlySanitizedErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	logger := slog.New(slog.DiscardHandler)
	enq, err := jobs.NewEnqueuer(pool, logger)
	if err != nil {
		t.Fatal(err)
	}
	e, err := emails.NewService(pool, enq, logger).Create(ctx, projectID, input)
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
		Pool: pool, Logger: logger, Concurrency: 1, JobTimeout: 10 * time.Second,
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
	if s := state(t, pool, e.ID); s.status != "sending" || !s.hasContent {
		t.Fatalf("after a temporary failure the email must wait for retry: %+v", s)
	}
}
