//go:build integration

package emails_test

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/events/ses"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/testaws"
)

// Use case 2 end to end with a real job worker: SES accepts the first
// attempt, but its answer never reaches the worker (the call times out). The
// retry finds no SES event yet and waits (asserted: the job snoozed); the
// send event then arrives through the event recorder, the call the queue
// poller makes, and the email is recorded with that message id. SES got
// exactly one call.
func TestLostAnswerIsReconciledWithoutSecondSend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	stub := testaws.NewSESv2(t)
	var calls atomic.Int32
	accepted := make(chan string, 1)
	stub.Respond(func(r testaws.SendEmail) testaws.Response {
		calls.Add(1)
		id := "msg-" + r.Tags["email_id"]
		select {
		case accepted <- id:
		default:
		}
		// Accepted, but the answer comes after the worker gave up.
		return testaws.Response{MessageID: id, Delay: 3 * time.Second}
	})
	logs := &safeBuffer{}
	rec := runReconcilingWorker(t, pool, stub, logs, 10*time.Second, 0)
	ids := enqueue(t, pool, projectID, 1)

	var msgID string
	select {
	case msgID = <-accepted:
	case <-time.After(15 * time.Second):
		t.Fatal("SES stub not called")
	}
	waitStatus(t, pool, ids[0], "sending", emails.CodeProviderTimeout, 10*time.Second)
	waitSnoozed(t, pool, ids[0], 15*time.Second) // the retry found no proof and waits
	// The SES send event of the accepted message arrives.
	if out, err := rec.Ingest(ctx, events.ProviderEvent{
		Provider: "ses", ProviderEventID: "send:" + msgID, ProviderMessageID: msgID,
		EmailID: ids[0], ProjectID: projectID, Type: "send", OccurredAt: time.Now(),
	}); err != nil || out != events.Stored {
		t.Fatalf("ingest send event: %v, %v", out, err)
	}
	waitStatus(t, pool, ids[0], "submitted", "", 30*time.Second)
	var providerID string
	if err := pool.QueryRow(ctx, "SELECT provider_id FROM emails WHERE id = $1", ids[0]).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 1 || providerID != msgID {
		t.Fatalf("SES calls %d, provider_id %q; want one call recorded as %q", n, providerID, msgID)
	}
	if strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Fatal("logged a possible duplicate although nothing was sent again")
	}
}

// Without an SES event after the window (healthy queue) the retry sends
// again: two calls, logged and recorded with the second message id.
func TestLostAnswerWithoutEventIsSentAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	stub := testaws.NewSESv2(t)
	var calls atomic.Int32
	var first, second atomic.Int64
	stub.Respond(func(testaws.SendEmail) testaws.Response {
		if calls.Add(1) == 1 {
			first.Store(time.Now().UnixNano())
			return testaws.Response{MessageID: "msg-first", Delay: 3 * time.Second}
		}
		second.Store(time.Now().UnixNano())
		return testaws.Response{MessageID: "msg-second"}
	})
	logs := &safeBuffer{}
	runReconcilingWorker(t, pool, stub, logs, 2*time.Second, 0)
	ids := enqueue(t, pool, projectID, 1)
	waitStatus(t, pool, ids[0], "submitted", "", 30*time.Second)
	var providerID string
	if err := pool.QueryRow(ctx, "SELECT provider_id FROM emails WHERE id = $1", ids[0]).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || providerID != "msg-second" || !strings.Contains(logs.String(), "possible_duplicate_send") {
		t.Fatalf("calls %d, provider_id %q; want a logged second send", calls.Load(), providerID)
	}
	// Not before the window after the failed attempt (1 s timeout + 2 s).
	if gap := time.Duration(second.Load() - first.Load()); gap < 2500*time.Millisecond {
		t.Fatalf("second send %v after the first, want the window waited", gap)
	}
}

// Use case 1 end to end: a worker died after SES accepted the email (job
// left running, email sending); the send event reached the event store
// meanwhile. The rescued job records the email with that message id and
// never calls SES.
func TestRescuedJobRecordsAcceptedSend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	stub := testaws.NewSESv2(t)
	var calls atomic.Int32
	stub.Respond(func(testaws.SendEmail) testaws.Response {
		calls.Add(1)
		return testaws.Response{MessageID: "msg-again"}
	})
	ids := enqueue(t, pool, projectID, 1)
	// The crash: job running since an hour, email mid-send.
	if _, err := pool.Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1,
		attempted_at = now() - interval '1 hour', attempted_by = '{crashed-worker}'
		WHERE args->>'email_id' = $1`, ids[0].String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE emails SET status = 'sending', attempts = 1, updated_at = now() - interval '1 hour' WHERE id = $1", ids[0]); err != nil {
		t.Fatal(err)
	}
	// The send event reached the store before the job is rescued.
	if out, err := events.NewRecorder(pool, slog.New(slog.DiscardHandler)).Ingest(ctx, events.ProviderEvent{
		Provider: "ses", ProviderEventID: "send:msg-crashed", ProviderMessageID: "msg-crashed",
		EmailID: ids[0], ProjectID: projectID, Type: "send", OccurredAt: time.Now().Add(-time.Hour),
	}); err != nil || out != events.Stored {
		t.Fatalf("ingest send event: %v, %v", out, err)
	}
	runReconcilingWorker(t, pool, stub, &safeBuffer{}, 20*time.Second, time.Second)
	waitStatus(t, pool, ids[0], "submitted", "", 30*time.Second)
	var providerID string
	if err := pool.QueryRow(ctx, "SELECT provider_id FROM emails WHERE id = $1", ids[0]).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 0 || providerID != "msg-crashed" {
		t.Fatalf("SES calls %d, provider_id %q; want none, recorded as msg-crashed", n, providerID)
	}
}

// runReconcilingWorker starts a real worker sending through the SES stub
// with reconciliation (window as given, healthy event queue) and returns
// its event recorder.
// rescueAfter 0 keeps the queue's default rescue time.
func runReconcilingWorker(t *testing.T, pool *pgxpool.Pool, stub *testaws.SESv2, logs *safeBuffer, window, rescueAfter time.Duration) *events.Recorder {
	t.Helper()
	p, err := delivery.NewSES(context.Background(), delivery.SESConfig{
		Region: "eu-central-1", ConfigurationSet: "platform-events", Endpoint: stub.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	d := emails.NewDispatcher(pool, p, logger, time.Second, 168*time.Hour)
	key := ses.QueueKey(reconcileQueue)
	emails.SetReconcile(d, emails.ReconcileConfig{Window: window, MaxWait: time.Minute, HealthyWithin: time.Minute, QueueKey: key})
	if _, err := pool.Exec(context.Background(), "INSERT INTO ses_queue_heartbeats (queue_key, drained_at) VALUES ($1, now())", key); err != nil {
		t.Fatal(err)
	}
	jobTimeout := 10 * time.Second
	if rescueAfter > 0 {
		jobTimeout = rescueAfter // the queue rescues no sooner than a job may run
	}
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: pool, Logger: logger, Concurrency: 2, JobTimeout: jobTimeout, RescueStuckJobsAfter: rescueAfter,
		Retry:    jobs.RetryPolicy{Base: 200 * time.Millisecond, Max: 400 * time.Millisecond},
		Handlers: jobs.Handlers{SendEmail: d.SendEmail, Cleanup: d.Cleanup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })
	return d.Events()
}

// waitSnoozed waits until the email's send job has snoozed at least once.
func waitSnoozed(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var n int
		if err := pool.QueryRow(context.Background(),
			"SELECT coalesce(max((metadata->>'snoozes')::int), 0) FROM river_job WHERE kind = 'send_email' AND args->>'email_id' = $1",
			id.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the retry never waited for SES events")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitStatus(t *testing.T, pool *pgxpool.Pool, id any, status, code string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var s string
		var c *string
		if err := pool.QueryRow(context.Background(), "SELECT status, error_code FROM emails WHERE id = $1", id).Scan(&s, &c); err != nil {
			t.Fatal(err)
		}
		if s == status && (code == "" || (c != nil && *c == code)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %s code %v, want %s %s", s, c, status, code)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
