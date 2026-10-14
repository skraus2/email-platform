//go:build integration

package emails_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
)

type storedEvent struct {
	typ, origin, provider string
	details               map[string]string
}

func eventsOf(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) []storedEvent {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT type, origin, coalesce(provider, ''), details FROM email_events WHERE email_id = $1 ORDER BY id", id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var e storedEvent
		var raw []byte
		if err := rows.Scan(&e.typ, &e.origin, &e.provider, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &e.details); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type changeLog struct {
	mu      sync.Mutex
	changes []events.Change
}

func (c *changeLog) hook(_ context.Context, _ pgx.Tx, ch events.Change) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.changes = append(c.changes, ch)
	return nil
}

func (c *changeLog) statuses() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ch := range c.changes {
		out = append(out, ch.Status)
	}
	return out
}

func TestDispatchRecordsPlatformEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	var log changeLog
	d := dispatcher(pool, &fakeProvider{send: captured}, nil)
	d.Events().OnStatusChange(log.hook)

	ok := queued(t, pool, projectID)
	if err := d.SendEmail(ctx, args(ok), attempt(1)); err != nil {
		t.Fatal(err)
	}
	// A late retry after the recorded success changes nothing.
	if err := d.SendEmail(ctx, args(ok), attempt(2)); err != nil {
		t.Fatal(err)
	}
	if ev := eventsOf(t, pool, ok.ID); len(ev) != 1 || ev[0].typ != "captured" || ev[0].origin != "platform" || ev[0].provider != "fake" || len(ev[0].details) != 0 {
		t.Fatalf("captured events = %+v", ev)
	}

	rejected := queued(t, pool, projectID)
	r := dispatcher(pool, &fakeProvider{send: func(context.Context, delivery.Message) (delivery.Result, error) {
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassRejected, Code: 550}
	}}, nil)
	r.Events().OnStatusChange(log.hook)
	if err := r.SendEmail(ctx, args(rejected), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if ev := eventsOf(t, pool, rejected.ID); len(ev) != 1 || ev[0].typ != "failed" || ev[0].provider != "" || ev[0].details["error_code"] != emails.CodeProviderRejected {
		t.Fatalf("failed events = %+v", ev)
	}

	// A temporary failure that will be retried is no status change.
	retried := queued(t, pool, projectID)
	tmp := dispatcher(pool, &fakeProvider{send: func(context.Context, delivery.Message) (delivery.Result, error) {
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassConnect}
	}}, nil)
	if err := tmp.SendEmail(ctx, args(retried), attempt(1)); err == nil {
		t.Fatal("temporary failure must be retried")
	}
	if ev := eventsOf(t, pool, retried.ID); len(ev) != 0 {
		t.Fatalf("events after a retryable failure = %+v, want none", ev)
	}

	if got := log.statuses(); len(got) != 2 || got[0] != "captured" || got[1] != "failed" {
		t.Fatalf("status changes = %v, want [captured failed]", got)
	}
}

func TestFinalInternalErrorRecordsFailedEvent(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	failMarkSending(t, pool)
	d := dispatcher(pool, &fakeProvider{send: captured}, nil)
	if err := d.SendEmail(context.Background(), args(e), attempt(testMaxAttempts)); err != nil {
		t.Fatal(err)
	}
	if ev := eventsOf(t, pool, e.ID); len(ev) != 1 || ev[0].typ != "failed" || ev[0].details["error_code"] != emails.CodeInternalError {
		t.Fatalf("events = %+v", ev)
	}
}

func TestExpiryRecordsFailedEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	var stale []emails.Email
	for range 3 {
		stale = append(stale, queued(t, pool, projectID))
	}
	fresh := queued(t, pool, projectID)
	if _, err := pool.Exec(ctx, "UPDATE emails SET created_at = now() - interval '8 days' WHERE id <> $1", fresh.ID); err != nil {
		t.Fatal(err)
	}
	var log changeLog
	d := dispatcher(pool, &fakeProvider{send: captured}, nil)
	d.Events().OnStatusChange(log.hook)
	if err := d.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, e := range stale {
		if ev := eventsOf(t, pool, e.ID); len(ev) != 1 || ev[0].typ != "failed" || ev[0].details["error_code"] != emails.CodeExpired {
			t.Fatalf("expired email events = %+v", ev)
		}
	}
	if ev := eventsOf(t, pool, fresh.ID); len(ev) != 0 {
		t.Fatalf("fresh email events = %+v", ev)
	}
	if got := log.statuses(); len(got) != 3 {
		t.Fatalf("status changes = %v, want 3 failed", got)
	}
	// A second run finds nothing to expire and records nothing.
	if err := d.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if got := log.statuses(); len(got) != 3 {
		t.Fatalf("second cleanup added changes: %v", got)
	}
}

// The worker's finish applies provider events that arrived while the email
// was still being sent.
func TestFinishProjectsEventsStoredDuringSend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	p := &fakeProvider{}
	d := dispatcher(pool, p, nil)
	var log changeLog
	d.Events().OnStatusChange(log.hook)
	p.send = func(c context.Context, _ delivery.Message) (delivery.Result, error) {
		out, err := d.Events().Ingest(c, events.ProviderEvent{
			Provider: "ses", ProviderEventID: "fb-1", ProviderMessageID: "msg-1",
			EmailID: e.ID, ProjectID: projectID, Type: "bounced", OccurredAt: time.Now(),
			Details: map[string]string{"bounce_type": "Permanent"},
		})
		if err != nil || out != events.Stored {
			t.Errorf("ingest during send = %v, %v", out, err)
		}
		return delivery.Result{Provider: "ses", ProviderID: "msg-1", Status: delivery.StatusSubmitted}, nil
	}
	if err := d.SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); s.status != "bounced" || s.hasContent {
		t.Fatalf("state = %+v, want bounced without content", s)
	}
	log.mu.Lock()
	changes := append([]events.Change(nil), log.changes...)
	log.mu.Unlock()
	var bounceID uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM email_events WHERE email_id = $1 AND type = 'bounced'", e.ID).Scan(&bounceID); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].Status != "submitted" || changes[1].Status != "bounced" || changes[1].EventID != bounceID {
		t.Fatalf("changes = %+v, want submitted then the stored bounce %s", changes, bounceID)
	}
}

// A hook error rolls the status change back: the email stays open for a
// retry instead of changing without its notification.
func TestHookErrorRollsBackTransition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	d := dispatcher(pool, &fakeProvider{send: func(context.Context, delivery.Message) (delivery.Result, error) {
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassRejected, Code: 550}
	}}, nil)
	d.Events().OnStatusChange(func(context.Context, pgx.Tx, events.Change) error { return context.DeadlineExceeded })
	if err := d.SendEmail(ctx, args(e), attempt(1)); err == nil {
		t.Fatal("hook failure must surface as a job error")
	}
	if s := state(t, pool, e.ID); s.status == "failed" {
		t.Fatalf("status = %s, want the failure rolled back", s.status)
	}
	if ev := eventsOf(t, pool, e.ID); len(ev) != 0 {
		t.Fatalf("events = %+v, want none", ev)
	}
}
