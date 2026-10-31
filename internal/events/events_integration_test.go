//go:build integration

package events_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

const recipient = "Customer@Example.test"

// eventTime is a fixed provider event time safely in the past.
var eventTime = time.Now().Add(-time.Hour).Truncate(time.Second).UTC()

type fixture struct {
	pool      *pgxpool.Pool
	projectID uuid.UUID
	rec       *events.Recorder
	logs      *bytes.Buffer
	mu        sync.Mutex
	changes   []events.Change
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.NewMigrated(t)
	p, err := auth.CreateProject(context.Background(), store.New(pool), "app")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, projectID: p.ID, logs: &bytes.Buffer{}}
	f.rec = events.NewRecorder(pool, slog.New(slog.NewJSONHandler(f.logs, nil)))
	f.rec.OnStatusChange(func(_ context.Context, _ pgx.Tx, c events.Change) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.changes = append(f.changes, c)
		return nil
	})
	return f
}

func (f *fixture) recorded() []events.Change {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]events.Change(nil), f.changes...)
}

// email inserts an email in the given state. provider "" leaves it NULL.
func (f *fixture) email(t *testing.T, status, provider string) uuid.UUID {
	t.Helper()
	var prov *string
	if provider != "" {
		prov = &provider
	}
	var id uuid.UUID
	err := f.pool.QueryRow(context.Background(), `
		INSERT INTO emails (project_id, sender, recipient, subject, status, provider, provider_id, error_code, finished_at)
		VALUES ($1, 'noreply@example.test', $2, 'Hi', $3, $4,
		        CASE WHEN $4::text IS NOT NULL THEN 'msg-1' END,
		        CASE WHEN $3 = 'failed' THEN 'expired' END,
		        CASE WHEN $3 IN ('queued', 'sending') THEN NULL ELSE now() END)
		RETURNING id`, f.projectID, recipient, status, prov).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) status(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), "SELECT status FROM emails WHERE id = $1", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) ev(emailID uuid.UUID, typ, eventID string, details map[string]string) events.ProviderEvent {
	return events.ProviderEvent{
		Provider: "ses", ProviderEventID: eventID, ProviderMessageID: "msg-1",
		EmailID: emailID, ProjectID: f.projectID, Type: typ,
		OccurredAt: eventTime, Details: details,
	}
}

func (f *fixture) ingest(t *testing.T, e events.ProviderEvent) events.Outcome {
	t.Helper()
	out, err := f.rec.Ingest(context.Background(), e)
	if err != nil {
		t.Fatalf("ingest %s: %v", e.Type, err)
	}
	return out
}

var (
	delivery = func(f *fixture, id uuid.UUID) events.ProviderEvent {
		return f.ev(id, "delivered", "delivery:msg-1", nil)
	}
	permanent = func(f *fixture, id uuid.UUID) events.ProviderEvent {
		return f.ev(id, "bounced", "fb-bounce", map[string]string{"bounce_type": "Permanent", "bounce_subtype": "General"})
	}
	complaint = func(f *fixture, id uuid.UUID) events.ProviderEvent {
		return f.ev(id, "complained", "fb-complaint", map[string]string{"complaint_feedback_type": "abuse"})
	}
)

func TestIngestStoresAndDeduplicates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "submitted", "ses")

	if out := f.ingest(t, delivery(f, id)); out != events.Stored {
		t.Fatalf("first = %v, want Stored", out)
	}
	if out := f.ingest(t, delivery(f, id)); out != events.Duplicate {
		t.Fatalf("second = %v, want Duplicate", out)
	}
	if got := f.status(t, id); got != "delivered" {
		t.Fatalf("status = %s, want delivered", got)
	}
	if n := f.count(t, "SELECT count(*) FROM email_events WHERE email_id = $1", id); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
	var origin, typ, provider, msgID string
	var occurred time.Time
	if err := f.pool.QueryRow(context.Background(),
		"SELECT origin, type, provider, provider_message_id, occurred_at FROM email_events WHERE email_id = $1", id,
	).Scan(&origin, &typ, &provider, &msgID, &occurred); err != nil {
		t.Fatal(err)
	}
	if origin != "ses" || typ != "delivered" || provider != "ses" || msgID != "msg-1" || !occurred.Equal(delivery(f, id).OccurredAt) {
		t.Fatalf("stored = %s %s %s %s %v", origin, typ, provider, msgID, occurred)
	}
	if c := f.recorded(); len(c) != 1 || c[0].Status != "delivered" || c[0].EmailID != id || c[0].ProjectID != f.projectID {
		t.Fatalf("changes = %+v, want one delivered", c)
	}
}

func TestConcurrentIngestOfOneEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "submitted", "ses")
	var stored atomic.Int32
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			out, err := f.rec.Ingest(context.Background(), permanent(f, id))
			if err != nil {
				t.Error(err)
			}
			if out == events.Stored {
				stored.Add(1)
			}
		})
	}
	wg.Wait()
	if stored.Load() != 1 {
		t.Fatalf("stored = %d, want exactly 1", stored.Load())
	}
	if n := f.count(t, "SELECT count(*) FROM email_events WHERE email_id = $1", id); n != 1 {
		t.Fatalf("events = %d, want 1", n)
	}
	if n := f.count(t, "SELECT count(*) FROM suppressions"); n != 1 {
		t.Fatalf("suppressions = %d, want 1", n)
	}
	if c := f.recorded(); len(c) != 1 {
		t.Fatalf("changes = %+v, want exactly one", c)
	}
}

func TestProjectionIsOrderIndependent(t *testing.T) {
	t.Parallel()
	all := []func(*fixture, uuid.UUID) events.ProviderEvent{delivery, permanent, complaint}
	perms := [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	for _, p := range perms {
		f := newFixture(t)
		id := f.email(t, "submitted", "ses")
		for _, i := range p {
			f.ingest(t, all[i](f, id))
		}
		if got := f.status(t, id); got != "complained" {
			t.Errorf("order %v: status = %s, want complained", p, got)
		}
	}
	for name, tc := range map[string]struct {
		order []func(*fixture, uuid.UUID) events.ProviderEvent
		want  string
	}{
		"late delivery after bounce": {[]func(*fixture, uuid.UUID) events.ProviderEvent{permanent, delivery}, "bounced"},
		"complaint after delivery":   {[]func(*fixture, uuid.UUID) events.ProviderEvent{delivery, complaint}, "complained"},
		"delivery twice":             {[]func(*fixture, uuid.UUID) events.ProviderEvent{delivery, delivery}, "delivered"},
	} {
		f := newFixture(t)
		id := f.email(t, "submitted", "ses")
		for _, e := range tc.order {
			f.ingest(t, e(f, id))
		}
		if got := f.status(t, id); got != tc.want {
			t.Errorf("%s: status = %s, want %s", name, got, tc.want)
		}
	}
}

// Only events that change the status reach the hook.
func TestHookOnlyForStatusChanges(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "submitted", "ses")
	f.ingest(t, f.ev(id, "send", "send:msg-1", nil))
	f.ingest(t, f.ev(id, "delivery_delayed", "delay:msg-1:1", map[string]string{"delay_type": "MailboxFull"}))
	f.ingest(t, permanent(f, id))
	f.ingest(t, permanent(f, id)) // duplicate
	f.ingest(t, delivery(f, id))  // late, lower rank
	f.ingest(t, f.ev(id, "rejected", "reject:msg-1", nil))
	c := f.recorded()
	if len(c) != 1 || c[0].Status != "bounced" {
		t.Fatalf("changes = %+v, want only bounced", c)
	}
	if got := f.status(t, id); got != "bounced" {
		t.Fatalf("status = %s", got)
	}
	if n := f.count(t, "SELECT count(*) FROM email_events WHERE email_id = $1", id); n != 5 {
		t.Fatalf("events = %d, want 5 distinct", n)
	}
}

func TestSuppressionFromEvents(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		event  func(*fixture, uuid.UUID) events.ProviderEvent
		reason string // "" = no suppression
	}{
		"permanent bounce": {permanent, "bounce"},
		"complaint":        {complaint, "complaint"},
		"transient bounce": {func(f *fixture, id uuid.UUID) events.ProviderEvent {
			return f.ev(id, "bounced", "fb-t", map[string]string{"bounce_type": "Transient"})
		}, ""},
		"undetermined bounce": {func(f *fixture, id uuid.UUID) events.ProviderEvent {
			return f.ev(id, "bounced", "fb-u", map[string]string{"bounce_type": "Undetermined"})
		}, ""},
		"delivery": {delivery, ""},
	} {
		f := newFixture(t)
		id := f.email(t, "submitted", "ses")
		f.ingest(t, tc.event(f, id))
		var reason string
		err := f.pool.QueryRow(context.Background(),
			"SELECT reason FROM suppressions WHERE project_id = $1 AND lower(recipient) = lower($2)", f.projectID, recipient,
		).Scan(&reason)
		switch {
		case tc.reason == "" && !errors.Is(err, pgx.ErrNoRows):
			t.Errorf("%s: suppression %q, %v; want none", name, reason, err)
		case tc.reason != "" && (err != nil || reason != tc.reason):
			t.Errorf("%s: suppression %q, %v; want %s", name, reason, err, tc.reason)
		}
	}

	// An existing suppression is kept as is.
	f := newFixture(t)
	id := f.email(t, "submitted", "ses")
	if _, err := store.New(f.pool).AddSuppression(context.Background(), store.AddSuppressionParams{
		ProjectID: f.projectID, Recipient: recipient, Reason: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	f.ingest(t, complaint(f, id))
	if n := f.count(t, "SELECT count(*) FROM suppressions WHERE reason = 'manual'"); n != 1 {
		t.Fatalf("manual suppression replaced (%d left)", n)
	}
}

func TestIngestRefusesMismatchAndUnknown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	captured := f.email(t, "captured", "mailpit")
	if out := f.ingest(t, delivery(f, captured)); out != events.ProviderMismatch {
		t.Fatalf("mailpit email = %v, want ProviderMismatch", out)
	}
	if got := f.status(t, captured); got != "captured" {
		t.Fatalf("status = %s, want captured", got)
	}

	other, err := auth.CreateProject(context.Background(), store.New(f.pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	sesEmail := f.email(t, "submitted", "ses")
	foreign := delivery(f, sesEmail)
	foreign.ProjectID = other.ID
	if out := f.ingest(t, foreign); out != events.UnknownEmail {
		t.Fatalf("foreign project = %v, want UnknownEmail", out)
	}
	missing := delivery(f, uuid.New())
	if out := f.ingest(t, missing); out != events.UnknownEmail {
		t.Fatalf("missing email = %v, want UnknownEmail", out)
	}
	if n := f.count(t, "SELECT count(*) FROM email_events"); n != 0 {
		t.Fatalf("events = %d, want none stored", n)
	}
	if got := f.status(t, sesEmail); got != "submitted" {
		t.Fatalf("status = %s, want submitted", got)
	}
	if n := f.count(t, "SELECT count(*) FROM suppressions"); n != 0 {
		t.Fatal("suppression added for a refused event")
	}
}

func TestIngestRejectsInvalidEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "submitted", "ses")
	long := strings.Repeat("x", 513)
	for name, mutate := range map[string]func(*events.ProviderEvent){
		"provider":          func(e *events.ProviderEvent) { e.Provider = "mailpit" },
		"platform type":     func(e *events.ProviderEvent) { e.Type = "captured" },
		"unknown type":      func(e *events.ProviderEvent) { e.Type = "opened" },
		"no event id":       func(e *events.ProviderEvent) { e.ProviderEventID = "" },
		"long event id":     func(e *events.ProviderEvent) { e.ProviderEventID = long },
		"long message id":   func(e *events.ProviderEvent) { e.ProviderMessageID = long },
		"no time":           func(e *events.ProviderEvent) { e.OccurredAt = time.Time{} },
		"detail key":        func(e *events.ProviderEvent) { e.Details = map[string]string{"recipient": "a@b.test"} },
		"detail value":      func(e *events.ProviderEvent) { e.Details = map[string]string{"bounce_type": long} },
		"detail control ch": func(e *events.ProviderEvent) { e.Details = map[string]string{"bounce_type": "a\nb"} },
		"nil email":         func(e *events.ProviderEvent) { e.EmailID = uuid.Nil },
		"future time":       func(e *events.ProviderEvent) { e.OccurredAt = time.Now().Add(2 * time.Hour) },
	} {
		e := delivery(f, id)
		mutate(&e)
		if _, err := f.rec.Ingest(context.Background(), e); !errors.Is(err, events.ErrInvalidEvent) {
			t.Errorf("%s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
	if n := f.count(t, "SELECT count(*) FROM email_events"); n != 0 {
		t.Fatalf("events = %d, want none", n)
	}
}

// Events for emails that are not dispatched yet are stored and wait; events
// for failed emails are stored and never change the status.
func TestEventsForOpenAndFailedEmails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	sending := f.email(t, "sending", "")
	if out := f.ingest(t, permanent(f, sending)); out != events.Stored {
		t.Fatalf("sending email = %v, want Stored", out)
	}
	if got := f.status(t, sending); got != "sending" {
		t.Fatalf("status = %s, want sending", got)
	}
	failed := f.email(t, "failed", "")
	if out := f.ingest(t, delivery(f, failed)); out != events.Stored {
		t.Fatalf("failed email = %v, want Stored", out)
	}
	if got := f.status(t, failed); got != "failed" {
		t.Fatalf("status = %s, want failed", got)
	}
	if !strings.Contains(f.logs.String(), "provider event for a failed email") {
		t.Error("no warning for an event of a failed email")
	}
	if strings.Contains(f.logs.String(), strings.ToLower(recipient)) || strings.Contains(f.logs.String(), recipient) {
		t.Error("recipient logged unmasked")
	}
	if c := f.recorded(); len(c) != 0 {
		t.Fatalf("changes = %+v, want none", c)
	}
}

// finish mimics the worker's finish transaction: the dispatched state, then
// the recorder.
func finish(ctx context.Context, f *fixture, id uuid.UUID, status, provider string) error {
	return pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).MarkDispatched(ctx, store.MarkDispatchedParams{
			ID: id, ProjectID: f.projectID, Status: status, Provider: &provider, ProviderID: new("msg-1"),
		})
		if err != nil || n == 0 {
			return err
		}
		_, err = f.rec.Dispatched(ctx, tx, f.projectID, id, status, provider, "msg-1")
		return err
	})
}

func TestDispatchedAppliesStoredEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "sending", "")
	f.ingest(t, delivery(f, id))
	f.ingest(t, permanent(f, id))
	if err := finish(context.Background(), f, id, "submitted", "ses"); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, id); got != "bounced" {
		t.Fatalf("status = %s, want bounced", got)
	}
	c := f.recorded()
	if len(c) != 2 || c[0].Status != "submitted" || c[1].Status != "bounced" {
		t.Fatalf("changes = %+v, want submitted then bounced", c)
	}
	var bounceID uuid.UUID
	if err := f.pool.QueryRow(context.Background(), "SELECT id FROM email_events WHERE type = 'bounced'").Scan(&bounceID); err != nil {
		t.Fatal(err)
	}
	if c[1].EventID != bounceID {
		t.Fatalf("bounced change points to %s, want the stored bounce %s", c[1].EventID, bounceID)
	}
	if n := f.count(t, "SELECT count(*) FROM email_events WHERE type = 'submitted' AND origin = 'platform' AND provider = 'ses'"); n != 1 {
		t.Fatalf("platform submitted events = %d, want 1", n)
	}
}

// A provider event stored while sending must not show on an email that the
// local test sink captured.
func TestCapturedNeverProjectsStoredEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "sending", "")
	f.ingest(t, delivery(f, id))
	if err := finish(context.Background(), f, id, "captured", "mailpit"); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, id); got != "captured" {
		t.Fatalf("status = %s, want captured", got)
	}
}

func TestNoFakeDeliveredInSchema(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, provider := range []string{"mailpit", ""} {
		id := f.email(t, "captured", provider)
		for _, status := range []string{"delivered", "bounced", "complained"} {
			_, err := f.pool.Exec(context.Background(), "UPDATE emails SET status = $2 WHERE id = $1", id, status)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.ConstraintName != "emails_provider_events_check" {
				t.Errorf("provider %q → %s: err = %v, want emails_provider_events_check", provider, status, err)
			}
		}
	}
}

// Lock order 1: the finish transaction holds the email row; an ingest
// waits, then sees the dispatched status and projects.
func TestIngestWaitsForFinish(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	id := f.email(t, "sending", "")

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit
	if _, err := store.New(tx).MarkDispatched(ctx, store.MarkDispatchedParams{
		ID: id, ProjectID: f.projectID, Status: "submitted", Provider: new("ses"), ProviderID: new("msg-1"),
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan events.Outcome, 1)
	go func() {
		out, err := f.rec.Ingest(ctx, permanent(f, id))
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	waitForLockWait(t, f.pool)
	if _, err := f.rec.Dispatched(ctx, tx, f.projectID, id, "submitted", "ses", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if out := <-done; out != events.Stored {
		t.Fatalf("ingest = %v", out)
	}
	if got := f.status(t, id); got != "bounced" {
		t.Fatalf("status = %s, want bounced", got)
	}
}

// Lock order 2: an ingest holds the email row with its event inserted but
// not committed; the finish waits and then applies the event.
func TestFinishWaitsForIngest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	id := f.email(t, "sending", "")

	inserted, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock) // never leave the ingest parked on a failed test
	events.SetAfterInsert(f.rec, func() {
		close(inserted)
		<-release
	})
	done := make(chan error, 1)
	go func() {
		_, err := f.rec.Ingest(ctx, permanent(f, id))
		done <- err
	}()
	select {
	case <-inserted:
	case err := <-done:
		t.Fatalf("ingest ended before inserting: %v", err)
	}
	finished := make(chan error, 1)
	go func() { finished <- finish(ctx, f, id, "submitted", "ses") }()
	waitForLockWait(t, f.pool)
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, id); got != "bounced" {
		t.Fatalf("status = %s, want bounced (event committed while finish waited)", got)
	}
}

// waitForLockWait blocks until some session of the test database waits for a
// row lock.
func waitForLockWait(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(context.Background(),
			"SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'",
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session waited for the lock")
}

type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueSendEmail(context.Context, pgx.Tx, jobs.SendEmailArgs, jobs.SendOpts) error {
	return nil
}

// After a permanent bounce, the next send to that recipient is refused.
func TestBounceRefusesNextSend(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "submitted", "ses")
	f.ingest(t, permanent(f, id))
	svc := emails.NewService(f.pool, noopEnqueuer{}, slog.New(slog.DiscardHandler), 1000)
	_, err := svc.Create(context.Background(), f.projectID, emails.CreateInput{
		From: "noreply@example.test", To: strings.ToLower(recipient), Subject: "Again", Text: "Hi",
	})
	if !errors.Is(err, emails.ErrRecipientSuppressed) {
		t.Fatalf("err = %v, want ErrRecipientSuppressed", err)
	}
}

// Stored events of SES are never projected onto an email another provider
// reports as submitted; the finish still commits.
func TestDispatchedIgnoresEventsOfAnotherProvider(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id := f.email(t, "sending", "")
	f.ingest(t, delivery(f, id))
	if err := finish(context.Background(), f, id, "submitted", "other"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := f.status(t, id); got != "submitted" {
		t.Fatalf("status = %s, want submitted", got)
	}
}

// A provider event id reused for another email is reported, not silently
// treated as a duplicate.
func TestReusedEventIDIsReported(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.email(t, "submitted", "ses")
	b := f.email(t, "submitted", "ses")
	f.ingest(t, permanent(f, a))
	if out := f.ingest(t, permanent(f, b)); out != events.Duplicate {
		t.Fatalf("outcome = %v", out)
	}
	if !strings.Contains(f.logs.String(), "provider event id already used by another email") {
		t.Fatal("no warning for a reused event id")
	}
	if got := f.status(t, b); got != "submitted" {
		t.Fatalf("other email status = %s", got)
	}
}

// A complaint or permanent bounce for an email that no longer exists still
// suppresses the addresses it names, in the tagged project only.
func TestIngestSuppressesDeletedEmail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	other, err := auth.CreateProject(ctx, store.New(f.pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	gone := uuid.New()
	suppressed := func(project uuid.UUID) []string {
		rows, err := f.pool.Query(ctx, "SELECT recipient || ':' || reason FROM suppressions WHERE project_id = $1 ORDER BY recipient", project)
		if err != nil {
			t.Fatal(err)
		}
		list, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	// Every log line of this test, for the leak check at the end.
	var allLogs strings.Builder
	// ingestLogged ingests e and returns the outcome and its log line.
	ingestLogged := func(e events.ProviderEvent) (events.Outcome, map[string]any) {
		t.Helper()
		f.logs.Reset()
		out := f.ingest(t, e)
		allLogs.WriteString(f.logs.String())
		var line map[string]any
		if err := json.Unmarshal(f.logs.Bytes(), &line); err != nil {
			t.Fatalf("log line %q: %v", f.logs.String(), err)
		}
		return out, line
	}

	complaint := f.ev(gone, "complained", "complaint-1", map[string]string{"complaint_feedback_type": "abuse"})
	complaint.Recipients = []string{"late-complainer@example.test"}
	complaint.RecipientsDropped = 2
	out, line := ingestLogged(complaint)
	if out != events.UnknownEmailSuppressed || line["suppressed"] != float64(1) || line["recipients_dropped"] != float64(2) {
		t.Fatalf("complaint = %v, log %v; want UnknownEmailSuppressed, suppressed 1, recipients_dropped 2", out, line)
	}
	bounce := f.ev(gone, "bounced", "bounce-1", map[string]string{"bounce_type": "Permanent"})
	bounce.Recipients = []string{"gone-mailbox@example.test", "Late-Complainer@Example.test"}
	// The case variant of an already suppressed address adds nothing.
	if out, line := ingestLogged(bounce); out != events.UnknownEmailSuppressed || line["suppressed"] != float64(1) {
		t.Fatalf("permanent bounce = %v, log %v; want suppressed 1", out, line)
	}
	want := []string{"gone-mailbox@example.test:bounce", "late-complainer@example.test:complaint"}
	if got := suppressed(f.projectID); !slices.Equal(got, want) {
		t.Fatalf("suppressions = %v, want %v", got, want)
	}
	if n := f.count(t, "SELECT count(*) FROM email_events"); n != 0 || len(f.recorded()) != 0 {
		t.Fatalf("events stored = %d, changes = %d; want none", n, len(f.recorded()))
	}

	// A redelivery changes nothing.
	if out, line := ingestLogged(complaint); out != events.UnknownEmailSuppressed || line["suppressed"] != float64(0) || len(suppressed(f.projectID)) != 2 {
		t.Fatalf("redelivery = %v, log %v, suppressions %v", out, line, suppressed(f.projectID))
	}

	// Not suppressing, no project, no addresses: nothing written.
	transient := f.ev(gone, "bounced", "bounce-2", map[string]string{"bounce_type": "Transient"})
	transient.Recipients = []string{"transient@example.test"}
	noProject := complaint
	noProject.ProjectID, noProject.ProviderEventID = uuid.New(), "complaint-2"
	noAddress := f.ev(gone, "complained", "complaint-3", nil)
	for name, c := range map[string]struct {
		e    events.ProviderEvent
		warn string
	}{
		"transient bounce":  {transient, ""},
		"unknown project":   {noProject, "suppressing event for unknown project"},
		"no usable address": {noAddress, "suppressing event without usable recipient"},
	} {
		out, line := ingestLogged(c.e)
		if out != events.UnknownEmail {
			t.Errorf("%s = %v, want UnknownEmail", name, out)
		}
		switch {
		case c.warn != "" && (line["msg"] != c.warn || line["level"] != "WARN"):
			t.Errorf("%s: log %v, want warning %q", name, line, c.warn)
		case c.warn == "" && line["level"] != "INFO":
			t.Errorf("%s: log %v, want an info line", name, line)
		}
	}
	if got := suppressed(f.projectID); len(got) != 2 {
		t.Fatalf("suppressions = %v", got)
	}
	if got := suppressed(other.ID); len(got) != 0 {
		t.Fatalf("other project suppressions = %v", got)
	}

	// A known email is suppressed by its stored recipient, never by the
	// payload's addresses.
	known := f.email(t, "submitted", "ses")
	kc := f.ev(known, "complained", "complaint-4", nil)
	kc.Recipients = []string{"someone-else@example.test"}
	if out, _ := ingestLogged(kc); out != events.Stored {
		t.Fatalf("known email complaint = %v", out)
	}
	got := suppressed(f.projectID)
	if !slices.Contains(got, recipient+":complaint") || slices.Contains(got, "someone-else@example.test:complaint") {
		t.Fatalf("suppressions = %v; want the stored recipient only", got)
	}

	for _, leak := range []string{"late-complainer", "gone-mailbox", "transient@", "someone-else"} {
		if strings.Contains(strings.ToLower(allLogs.String()), leak) {
			t.Errorf("logs contain %q", leak)
		}
	}
}

// An ingest waiting for an email that is deleted meanwhile (retention) takes
// the deleted-email path.
func TestIngestSuppressesEmailDeletedWhileWaiting(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	id := f.email(t, "submitted", "ses")

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, "DELETE FROM emails WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	e := f.ev(id, "complained", "complaint-1", nil)
	e.Recipients = []string{"waiting@example.test"}
	type result struct {
		out events.Outcome
		err error
	}
	done := make(chan result, 1)
	ictx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	go func() {
		out, err := f.rec.Ingest(ictx, e)
		done <- result{out, err}
	}()
	// Commit only once the ingest is really waiting for the email row lock.
	waitForLockWait(t, f.pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.out != events.UnknownEmailSuppressed {
		t.Fatalf("outcome = %v, %v; want UnknownEmailSuppressed", r.out, r.err)
	}
	if n := f.count(t, "SELECT count(*) FROM suppressions WHERE recipient = 'waiting@example.test'"); n != 1 {
		t.Fatalf("suppressions = %d, want 1", n)
	}
}
