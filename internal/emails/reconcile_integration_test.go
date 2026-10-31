//go:build integration

package emails_test

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/events/ses"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/metrics"
	"github.com/skraus2/email-platform/internal/testaws"
	"github.com/skraus2/email-platform/internal/testmailpit"
)

const reconcileQueue = "https://sqs.eu-central-1.amazonaws.com/123456789012/ses-events"

// reconcileSetup is an SES dispatcher with reconciliation (5 min window,
// 1 h max wait, 2 min health), a stub that counts sends, metrics and a log
// of status changes.
type reconcileSetup struct {
	pool      *pgxpool.Pool
	projectID uuid.UUID
	d         *emails.Dispatcher
	sends     *atomic.Int32
	reg       *prometheus.Registry
	logs      *bytes.Buffer
	changes   *atomic.Int32
}

func newReconcileSetup(t *testing.T) *reconcileSetup {
	t.Helper()
	pool, projectID := setup(t)
	stub := testaws.NewSESv2(t)
	sends := &atomic.Int32{}
	stub.Respond(func(testaws.SendEmail) testaws.Response {
		sends.Add(1)
		return testaws.Response{MessageID: "msg-resent"}
	})
	logs := &bytes.Buffer{}
	d := sesDispatcher(t, pool, stub, 5*time.Second, logs)
	if err := d.Reconcile(emails.ReconcileConfig{
		Window: 5 * time.Minute, MaxWait: time.Hour, HealthyWithin: 2 * time.Minute, QueueKey: ses.QueueKey(reconcileQueue),
	}); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	d.Observe(metrics.NewDispatch(reg, emails.FailureCodes), "ses")
	dups := metrics.NewDuplicates(reg)
	d.ObserveDuplicates(dups)
	changes := &atomic.Int32{}
	d.Events().OnStatusChange(func(context.Context, pgx.Tx, events.Change) error { changes.Add(1); return nil })
	return &reconcileSetup{pool: pool, projectID: projectID, d: d, sends: sends, reg: reg, logs: logs, changes: changes}
}

// leftSending creates an email an interrupted attempt left "sending" age ago.
func (r *reconcileSetup) leftSending(t *testing.T, age time.Duration) emails.Email {
	t.Helper()
	e := queued(t, r.pool, r.projectID)
	if _, err := r.pool.Exec(context.Background(),
		"UPDATE emails SET status = 'sending', attempts = 1, updated_at = now() - make_interval(secs => $2) WHERE id = $1",
		e.ID, age.Seconds()); err != nil {
		t.Fatal(err)
	}
	return e
}

// sesEvent stores an SES event of the email as the poller would.
func (r *reconcileSetup) sesEvent(t *testing.T, projectID, emailID uuid.UUID, typ, msgID string, occurred time.Time) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(), `
		INSERT INTO email_events (project_id, email_id, origin, type, provider, provider_event_id, provider_message_id, occurred_at, details)
		VALUES ($1, $2, 'ses', $3, 'ses', $4, $5, $6, '{}')`,
		projectID, emailID, typ, typ+":"+msgID+":"+uuid.NewString(), msgID, occurred); err != nil {
		t.Fatal(err)
	}
}

// drained records that a worker drained the event queue ago.
func (r *reconcileSetup) drained(t *testing.T, ago time.Duration) {
	t.Helper()
	if _, err := r.pool.Exec(context.Background(), `
		INSERT INTO ses_queue_heartbeats (queue_key, drained_at) VALUES ($1, now() - make_interval(secs => $2))
		ON CONFLICT (queue_key) DO UPDATE SET drained_at = EXCLUDED.drained_at`,
		ses.QueueKey(reconcileQueue), ago.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func (r *reconcileSetup) run(t *testing.T, e emails.Email) error {
	t.Helper()
	return r.d.SendEmail(context.Background(), args(e), attempt(2))
}

// runFinal runs the job's last attempt: a wait must still be a snooze.
func (r *reconcileSetup) runFinal(t *testing.T, e emails.Email) error {
	t.Helper()
	return r.d.SendEmail(context.Background(), args(e), attempt(testMaxAttempts))
}

// counter reads one sample: name{label=value}.
func (r *reconcileSetup) counter(t *testing.T, name, label, value string) float64 {
	t.Helper()
	families, err := r.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatalf("no sample %s{%s=%q}", name, label, value)
	return 0
}

func (r *reconcileSetup) reconciled(t *testing.T, result string) float64 {
	t.Helper()
	return r.counter(t, "email_platform_dispatch_reconciliations_total", "result", result)
}

func (r *reconcileSetup) duplicates(t *testing.T, kind string) float64 {
	t.Helper()
	return r.counter(t, "email_platform_duplicate_sends_total", "kind", kind)
}

func (r *reconcileSetup) providerID(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var p *string
	if err := r.pool.QueryRow(context.Background(), "SELECT provider_id FROM emails WHERE id = $1", id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	if p == nil {
		return ""
	}
	return *p
}

// row is the part of the email row a snooze must not touch.
func (r *reconcileSetup) row(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := r.pool.QueryRow(context.Background(),
		"SELECT status || '|' || attempts || '|' || updated_at::text || '|' || coalesce(error_code, '') FROM emails WHERE id = $1", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func snoozed(t *testing.T, err error, lo, hi time.Duration) {
	t.Helper()
	d, ok := jobs.SnoozeDelay(err)
	if !ok || d < lo || d > hi {
		t.Fatalf("result = %v (snooze %v, %v), want a snooze between %v and %v", err, d, ok, lo, hi)
	}
}

// AC1: a stored SES send event proves SES accepted the email: recorded as
// handed off with that message id, nothing sent.
func TestReconcileFoundSendEvent(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	e := r.leftSending(t, time.Minute) // proof counts even within the window
	r.sesEvent(t, r.projectID, e.ID, "send", "msg-accepted", time.Now().Add(-time.Minute))
	if err := r.run(t, e); err != nil {
		t.Fatal(err)
	}
	s := state(t, r.pool, e.ID)
	if r.sends.Load() != 0 || s.status != "submitted" || s.hasContent || !s.finished || s.attempts != 1 {
		t.Fatalf("sends %d, state %+v; want no send, submitted, content deleted, attempts unchanged", r.sends.Load(), s)
	}
	if got := r.providerID(t, e.ID); got != "msg-accepted" {
		t.Fatalf("provider_id = %q", got)
	}
	if r.changes.Load() != 1 || count(t, r.pool, "SELECT count(*) FROM email_events WHERE origin = 'platform' AND type = 'submitted' AND provider = 'ses'") != 1 {
		t.Fatalf("status changes %d; want one submitted change and event", r.changes.Load())
	}
	if r.reconciled(t, emails.ReconcileFound) != 1 || r.duplicates(t, "possible") != 0 {
		t.Fatal("metrics: want found 1, possible 0")
	}
}

// AC2 + OD-a: a delivered event stored before is projected; a rejected one
// alone counts as handed off.
func TestReconcileFoundLaterEvents(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	delivered := r.leftSending(t, 10*time.Minute)
	r.sesEvent(t, r.projectID, delivered.ID, "delivered", "msg-1", time.Now().Add(-5*time.Minute))
	rejected := r.leftSending(t, 10*time.Minute)
	r.sesEvent(t, r.projectID, rejected.ID, "rejected", "msg-2", time.Now().Add(-5*time.Minute))
	for _, e := range []emails.Email{delivered, rejected} {
		if err := r.run(t, e); err != nil {
			t.Fatal(err)
		}
	}
	if s := state(t, r.pool, delivered.ID).status; s != "delivered" {
		t.Fatalf("delivered email = %s", s)
	}
	if s := state(t, r.pool, rejected.ID).status; s != "submitted" || r.providerID(t, rejected.ID) != "msg-2" {
		t.Fatalf("rejected email = %s %s, want submitted with its message id", s, r.providerID(t, rejected.ID))
	}
	if r.sends.Load() != 0 {
		t.Fatalf("sends = %d", r.sends.Load())
	}
}

// AC3: no proof within the window: the job waits for the rest of it and
// leaves the email row as it was.
func TestReconcileWaitsWithinWindow(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	r.drained(t, 0)
	e := r.leftSending(t, time.Minute)
	before := r.row(t, e.ID)
	snoozed(t, r.run(t, e), 3*time.Minute+30*time.Second, 4*time.Minute+31*time.Second)
	if after := r.row(t, e.ID); after != before || r.sends.Load() != 0 {
		t.Fatalf("row %q → %q, sends %d; want untouched", before, after, r.sends.Load())
	}
	if r.reconciled(t, emails.ReconcileWaiting) != 1 || r.counter(t, "email_platform_dispatch_total", "outcome", "snoozed") != 1 {
		t.Fatal("metrics: want waiting 1 and a snoozed outcome")
	}
	// The last attempt waits the same way (a snooze never fails the job).
	snoozed(t, r.runFinal(t, e), time.Minute, 4*time.Minute+31*time.Second)
	if s := state(t, r.pool, e.ID); s.status != "sending" || r.sends.Load() != 0 {
		t.Fatalf("final attempt: %+v, sends %d", s, r.sends.Load())
	}
}

// AC4: no proof after the window with a healthy event queue: sent again,
// counted as a possible duplicate.
func TestReconcileResendsWhenQueueHealthy(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	r.drained(t, 30*time.Second)
	e := r.leftSending(t, 6*time.Minute)
	if err := r.run(t, e); err != nil {
		t.Fatal(err)
	}
	if r.sends.Load() != 1 || state(t, r.pool, e.ID).status != "submitted" || r.providerID(t, e.ID) != "msg-resent" {
		t.Fatalf("sends %d, state %+v", r.sends.Load(), state(t, r.pool, e.ID))
	}
	if r.reconciled(t, emails.ReconcileResent) != 1 || r.duplicates(t, "possible") != 1 {
		t.Fatal("metrics: want resent 1, possible 1")
	}
	if !strings.Contains(r.logs.String(), "possible_duplicate_send") {
		t.Fatal("possible_duplicate_send not logged")
	}
}

// AC5: without a healthy queue the job keeps waiting in 5-minute steps up to
// the max wait, then sends.
func TestReconcileWaitsForUnhealthyQueue(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	r.drained(t, 3*time.Minute) // older than the 2-minute health window
	e := r.leftSending(t, 6*time.Minute)
	before := r.row(t, e.ID)
	snoozed(t, r.run(t, e), 4*time.Minute, 5*time.Minute+31*time.Second)
	if r.row(t, e.ID) != before || r.reconciled(t, emails.ReconcilePipelineUnhealthy) != 1 {
		t.Fatal("want an untouched row and pipeline_unhealthy")
	}
	snoozed(t, r.runFinal(t, e), 4*time.Minute, 5*time.Minute+31*time.Second) // also on the last attempt
	near := r.leftSending(t, 58*time.Minute)                                  // waits only up to the max wait
	snoozed(t, r.run(t, near), 90*time.Second, 2*time.Minute+31*time.Second)
	old := r.leftSending(t, 61*time.Minute)
	if err := r.run(t, old); err != nil {
		t.Fatal(err)
	}
	if r.sends.Load() != 1 || state(t, r.pool, old.ID).status != "submitted" {
		t.Fatalf("after the max wait: sends %d, %+v", r.sends.Load(), state(t, r.pool, old.ID))
	}
}

// AC6: proof wins over a refusal reason: suppressed after SES accepted it,
// the email is still recorded as handed off.
func TestReconcileFoundWhileSuppressed(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	e := r.leftSending(t, 10*time.Minute)
	r.sesEvent(t, r.projectID, e.ID, "send", "msg-out", time.Now().Add(-9*time.Minute))
	if _, err := r.pool.Exec(context.Background(), "INSERT INTO suppressions (project_id, recipient, reason) VALUES ($1, $2, 'manual')", r.projectID, input.To); err != nil {
		t.Fatal(err)
	}
	if err := r.run(t, e); err != nil {
		t.Fatal(err)
	}
	if s := state(t, r.pool, e.ID); s.status != "submitted" || r.sends.Load() != 0 {
		t.Fatalf("state %+v, sends %d; want submitted, no send", s, r.sends.Load())
	}
}

// AC7: a refusal reason without proof applies only after the window.
func TestReconcileRefusesOnlyAfterWindow(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	r.drained(t, 0)
	young := r.leftSending(t, time.Minute)
	old := r.leftSending(t, 6*time.Minute)
	if _, err := r.pool.Exec(context.Background(), "UPDATE projects SET status = 'disabled' WHERE id = $1", r.projectID); err != nil {
		t.Fatal(err)
	}
	snoozed(t, r.run(t, young), time.Minute, 5*time.Minute)
	if s := state(t, r.pool, young.ID); s.status != "sending" {
		t.Fatalf("within the window: %+v, want still sending", s)
	}
	if err := r.run(t, old); err != nil {
		t.Fatal(err)
	}
	if s := state(t, r.pool, old.ID); s.status != "failed" || s.code() != emails.CodeProjectDisabled || r.sends.Load() != 0 {
		t.Fatalf("after the window: %+v, sends %d", s, r.sends.Load())
	}
}

// AC8: several message ids: the earliest by occurrence is recorded, the
// others are confirmed duplicates.
func TestReconcileEarliestOfSeveral(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	e := r.leftSending(t, 10*time.Minute)
	now := time.Now()
	r.sesEvent(t, r.projectID, e.ID, "send", "msg-late", now.Add(-2*time.Minute))
	r.sesEvent(t, r.projectID, e.ID, "send", "msg-early", now.Add(-9*time.Minute))
	r.sesEvent(t, r.projectID, e.ID, "delivered", "msg-late", now.Add(-time.Minute))
	if err := r.run(t, e); err != nil {
		t.Fatal(err)
	}
	if got := r.providerID(t, e.ID); got != "msg-early" {
		t.Fatalf("provider_id = %q, want the earliest", got)
	}
	if r.duplicates(t, "confirmed") != 1 {
		t.Fatalf("confirmed = %v, want 1", r.duplicates(t, "confirmed"))
	}
}

// AC10: expired between the evidence read and the record: skipped.
func TestReconcileFoundButExpiredMeanwhile(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	e := r.leftSending(t, 10*time.Minute)
	r.sesEvent(t, r.projectID, e.ID, "send", "msg-out", time.Now().Add(-9*time.Minute))
	emails.SetAfterReconcileRead(r.d, func() {
		if _, err := r.pool.Exec(context.Background(),
			"UPDATE emails SET status = 'failed', error_code = 'expired', last_error = 'expired', finished_at = now() WHERE id = $1", e.ID); err != nil {
			t.Error(err)
		}
	})
	if err := r.run(t, e); err != nil {
		t.Fatalf("want skipped without error, got %v", err)
	}
	if s := state(t, r.pool, e.ID); s.status != "failed" || r.counter(t, "email_platform_dispatch_total", "outcome", "skipped") != 1 {
		t.Fatalf("state %+v; want failed kept and a skipped outcome", s)
	}
}

// AC17: another project's events never count for an email. The schema
// already makes an event of another project for the same email impossible
// (composite key to emails); this shows a foreign email's event does not
// leak into the evidence of this one.
func TestReconcileIgnoresOtherProjects(t *testing.T) {
	t.Parallel()
	r := newReconcileSetup(t)
	r.drained(t, 0)
	e := r.leftSending(t, 6*time.Minute)
	otherProject := uuid.New()
	if _, err := r.pool.Exec(context.Background(), "INSERT INTO projects (id, name) VALUES ($1, 'other')", otherProject); err != nil {
		t.Fatal(err)
	}
	var other uuid.UUID
	if err := r.pool.QueryRow(context.Background(), `
		INSERT INTO emails (project_id, sender, recipient, subject, status) VALUES ($1, 'a@example.test', 'b@example.test', 'x', 'sending')
		RETURNING id`, otherProject).Scan(&other); err != nil {
		t.Fatal(err)
	}
	r.sesEvent(t, otherProject, other, "send", "msg-foreign", time.Now().Add(-5*time.Minute))
	if err := r.run(t, e); err != nil {
		t.Fatal(err)
	}
	if r.providerID(t, e.ID) != "msg-resent" || r.sends.Load() != 1 {
		t.Fatalf("provider_id %q, sends %d; want the foreign event ignored", r.providerID(t, e.ID), r.sends.Load())
	}
}

// AC14: without reconciliation (the local test sink) an email left
// "sending" is sent again at once, counted as a possible duplicate.
func TestMailpitResendsAtOnce(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	mp, err := delivery.NewMailpit(testmailpit.SMTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	d := dispatcher(pool, mp, nil)
	reg := prometheus.NewRegistry()
	d.Observe(metrics.NewDispatch(reg, emails.FailureCodes), "mailpit")
	d.ObserveDuplicates(metrics.NewDuplicates(reg))
	e := queued(t, pool, projectID)
	if _, err := pool.Exec(context.Background(), "UPDATE emails SET status = 'sending', attempts = 1, updated_at = now() WHERE id = $1", e.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SendEmail(context.Background(), args(e), attempt(2)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); s.status != "captured" || s.attempts != 2 {
		t.Fatalf("state = %+v, want captured on the second attempt", s)
	}
	r := &reconcileSetup{reg: reg}
	if r.duplicates(t, "possible") != 1 || r.reconciled(t, emails.ReconcileWaiting) != 0 || r.reconciled(t, emails.ReconcileResent) != 0 {
		t.Fatal("metrics: want possible 1 and no reconciliation")
	}
}

// AC18 (database side): no trigger writes emails, so only the queries the
// store audit checks can move updated_at of an email left sending.
func TestNoTriggersOnEmails(t *testing.T) {
	t.Parallel()
	pool, _ := setup(t)
	if n := count(t, pool, "SELECT count(*) FROM pg_trigger WHERE tgrelid = 'emails'::regclass AND NOT tgisinternal"); n != 0 {
		t.Fatalf("%d triggers on emails", n)
	}
}
