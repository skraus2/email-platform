//go:build integration

package broadcasts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/templates"
)

// failingSends fails the first n calls, then passes on to next.
type failingSends struct {
	n    int
	next broadcasts.SendEnqueuer
}

func (f *failingSends) EnqueueSendEmails(ctx context.Context, tx pgx.Tx, args []jobs.SendEmailArgs, opts jobs.SendOpts) error {
	if f.n > 0 {
		f.n--
		return errors.New("queue unavailable")
	}
	return f.next.EnqueueSendEmails(ctx, tx, args, opts)
}

type audienceSetup struct {
	ids map[string]uuid.UUID // by email
}

// members adds contacts to the fixture's audience: four subscribed with a
// name, one unsubscribed, one suppressed, one without the required name.
func (f *fixture) members(t *testing.T) audienceSetup {
	t.Helper()
	cs := contacts.NewStore(f.pool, 1000)
	au := contacts.NewAudiences(f.pool, 10, 1000)
	src := "signup"
	out := audienceSetup{ids: map[string]uuid.UUID{}}
	add := func(email, status, attrs string) {
		in := contacts.CreateInput{Email: email, Status: status, Attributes: json.RawMessage(attrs)}
		if status == contacts.StatusSubscribed {
			in.ConsentSource = &src
		}
		c, err := cs.Create(f.ctx, f.project, in)
		if err != nil {
			t.Fatal(err)
		}
		if err := au.AddOne(f.ctx, f.project, f.audience, c.ID); err != nil {
			t.Fatal(err)
		}
		out.ids[email] = c.ID
	}
	for _, n := range []string{"ada", "bob", "cy", "dee"} {
		add(n+"@example.test", contacts.StatusSubscribed, `{"name":"`+strings.ToUpper(n)+`","plan":"own"}`)
	}
	add("gone@example.test", contacts.StatusUnsubscribed, `{"name":"Gone"}`)
	add("blocked@example.test", contacts.StatusSubscribed, `{"name":"Blocked"}`)
	add("nameless@example.test", contacts.StatusSubscribed, `{}`)
	if _, err := f.pool.Exec(f.ctx, "INSERT INTO suppressions (project_id, recipient, reason) VALUES ($1, 'BLOCKED@example.test', 'bounce')", f.project); err != nil {
		t.Fatal(err)
	}
	return out
}

// requiredName publishes a version that needs a name, and returns a
// scheduled broadcast of it.
func (f *fixture) scheduled(t *testing.T) broadcasts.Broadcast {
	t.Helper()
	schema := `{"unsubscribe_url":{"type":"string","required":true},"name":{"type":"string","required":true},"plan":{"type":"string"}}`
	if _, err := f.templates.Publish(f.ctx, f.project, f.template, templates.Source{
		Subject: "News for {{.name}} ({{.plan}})", Text: "Hi {{.name}} {{.unsubscribe_url}}",
	}, []byte(schema)); err != nil {
		t.Fatal(err)
	}
	b, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Schedule(f.ctx, f.project, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fixture) fanout(t *testing.T, enq broadcasts.SendEnqueuer, logs *bytes.Buffer) *broadcasts.Fanout {
	t.Helper()
	if enq == nil {
		e, err := jobs.NewEnqueuer(f.pool, slog.New(slog.DiscardHandler), 5)
		if err != nil {
			t.Fatal(err)
		}
		enq = e
	}
	if logs == nil {
		logs = &bytes.Buffer{}
	}
	fo := broadcasts.NewFanout(f.pool, enq, slog.New(slog.NewJSONHandler(logs, nil)), templates.NewCache(1<<20, 64<<10))
	fo.SetBatch(3)
	return fo
}

// runAll runs the handler until it stops snoozing; it returns the runs.
func runAll(t *testing.T, fo *broadcasts.Fanout, b broadcasts.Broadcast) int {
	t.Helper()
	for i := 1; i <= 20; i++ {
		err := fo.Run(context.Background(), jobs.BroadcastFanoutArgs{BroadcastID: b.ID, ProjectID: b.ProjectID}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 25})
		if err == nil {
			return i
		}
		if d, ok := jobs.SnoozeDelay(err); !ok || d != 0 {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	t.Fatal("fan-out did not finish")
	return 0
}

func TestFanoutCreatesOneEmailPerSubscribedMember(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	m := f.members(t)
	b := f.scheduled(t)
	if runs := runAll(t, f.fanout(t, nil, nil), b); runs != 3 { // 7 members in batches of 3
		t.Fatalf("runs = %d", runs)
	}
	got, err := f.store.Get(f.ctx, f.project, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Counts
	if got.Status != broadcasts.StatusCompleted || got.StartedAt == nil || got.FinishedAt == nil ||
		c.Recipients != 4 || c.SkippedUnsubscribed != 1 || c.SkippedSuppressed != 1 || c.SkippedInvalid != 1 || c.ByStatus["queued"] != 4 {
		t.Fatalf("broadcast: %+v %+v", got, c)
	}
	rows, err := f.pool.Query(f.ctx, `SELECT recipient, subject, contact_id FROM emails WHERE broadcast_id = $1 ORDER BY recipient`, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var r, s string
		var cid uuid.UUID
		if err := rows.Scan(&r, &s, &cid); err != nil {
			t.Fatal(err)
		}
		if m.ids[r] != cid {
			t.Errorf("%s has contact %s", r, cid)
		}
		lines = append(lines, r+"|"+s)
	}
	want := "ada@example.test|News for ADA (own),bob@example.test|News for BOB (own),cy@example.test|News for CY (own),dee@example.test|News for DEE (own)"
	if strings.Join(lines, ",") != want {
		t.Fatalf("emails %v", lines)
	}
	// No content at rest; one bulk-priority dispatch job per email.
	if n := f.count(t, "SELECT count(*) FROM email_contents"); n != 0 {
		t.Fatalf("contents stored: %d", n)
	}
	if n := f.count(t, "SELECT count(*) FROM river_job WHERE kind = 'send_email' AND priority = 2"); n != 4 {
		t.Fatalf("bulk jobs: %d", n)
	}
	// Broadcast emails do not use up the project's API send rate.
	if n := f.count(t, "SELECT count(*) FROM rate_limit_windows"); n != 0 {
		t.Fatalf("send rate windows: %d", n)
	}
	// A further run changes nothing.
	if runs := runAll(t, f.fanout(t, nil, nil), b); runs != 1 || f.count(t, "SELECT count(*) FROM emails") != 4 {
		t.Fatal("re-run after completion created emails")
	}
	// Even a repeat of every batch (cursor lost) creates no second email.
	if _, err := f.pool.Exec(f.ctx, "UPDATE broadcasts SET status = 'sending', finished_at = NULL, cursor_contact_id = NULL WHERE id = $1", b.ID); err != nil {
		t.Fatal(err)
	}
	runAll(t, f.fanout(t, nil, nil), b)
	if f.count(t, "SELECT count(*) FROM emails") != 4 || f.count(t, "SELECT count(*) FROM river_job WHERE kind = 'send_email'") != 4 {
		t.Fatal("a repeated batch created emails or jobs twice")
	}
}

func TestFanoutResumesAfterFailedBatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	f.members(t)
	b := f.scheduled(t)
	enq, err := jobs.NewEnqueuer(f.pool, slog.New(slog.DiscardHandler), 5)
	if err != nil {
		t.Fatal(err)
	}
	failing := &failingSends{n: 1, next: enq}
	fo := f.fanout(t, failing, nil)
	args := jobs.BroadcastFanoutArgs{BroadcastID: b.ID, ProjectID: f.project}
	// The first batch fails: nothing of it stays.
	if err := fo.Run(f.ctx, args, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 25}); err == nil {
		t.Fatal("failure not reported")
	}
	if n := f.count(t, "SELECT count(*) FROM emails"); n != 0 {
		t.Fatalf("failed batch left %d emails", n)
	}
	runAll(t, fo, b)
	got, _ := f.store.Get(f.ctx, f.project, b.ID)
	if c := got.Counts; c.Recipients != 4 || c.SkippedUnsubscribed+c.SkippedSuppressed+c.SkippedInvalid != 3 || f.count(t, "SELECT count(*) FROM emails") != 4 ||
		f.count(t, "SELECT count(*) FROM river_job WHERE kind = 'send_email'") != 4 {
		t.Fatalf("after resume: %+v", c)
	}
}

func TestFanoutFinalFailureIsLogged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	f.members(t)
	b := f.scheduled(t)
	var logs bytes.Buffer
	fo := f.fanout(t, &failingSends{n: 1}, &logs)
	err := fo.Run(f.ctx, jobs.BroadcastFanoutArgs{BroadcastID: b.ID, ProjectID: f.project}, jobs.Attempt{JobID: 1, Number: 25, MaxAttempts: 25})
	if err == nil || !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "final attempt") {
		t.Fatalf("final failure: %v %s", err, logs.String())
	}
	if strings.Contains(logs.String(), "example.test") {
		t.Fatal("log holds recipient addresses")
	}
	// It stays sending and can be cancelled.
	got, _ := f.store.Get(f.ctx, f.project, b.ID)
	if got.Status != broadcasts.StatusScheduled && got.Status != broadcasts.StatusSending {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := f.store.Cancel(f.ctx, f.project, b.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFanoutStopsWhenCanceled(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	f.members(t)
	b := f.scheduled(t)
	fo := f.fanout(t, nil, nil)
	args := jobs.BroadcastFanoutArgs{BroadcastID: b.ID, ProjectID: f.project}
	if err := fo.Run(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err == nil {
		t.Fatal("first batch should snooze")
	}
	first := f.count(t, "SELECT count(*) FROM emails")
	if _, err := f.store.Cancel(f.ctx, f.project, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := fo.Run(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
	if n := f.count(t, "SELECT count(*) FROM emails"); n != first {
		t.Fatalf("emails after cancel: %d, before %d", n, first)
	}
}

func TestFanoutStopsForADisabledProject(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	f.members(t)
	b := f.scheduled(t)
	if _, err := f.pool.Exec(f.ctx, "UPDATE projects SET status = 'disabled' WHERE id = $1", f.project); err != nil {
		t.Fatal(err)
	}
	fo := f.fanout(t, nil, nil)
	args := jobs.BroadcastFanoutArgs{BroadcastID: b.ID, ProjectID: f.project}
	if err := fo.Run(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatalf("disabled project: %v (want the job to end)", err)
	}
	if n := f.count(t, "SELECT count(*) FROM emails"); n != 0 {
		t.Fatalf("emails for a disabled project: %d", n)
	}
	// Re-enabling does not resume it by itself; it stays open to cancel.
	if _, err := f.pool.Exec(f.ctx, "UPDATE projects SET status = 'active' WHERE id = $1", f.project); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.Get(f.ctx, f.project, b.ID); got.Status == broadcasts.StatusCompleted || got.Status == broadcasts.StatusCanceled {
		t.Fatalf("status %s", got.Status)
	}
	if _, err := f.store.Cancel(f.ctx, f.project, b.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFanoutIgnoresForeignProject(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	f.members(t)
	b := f.scheduled(t)
	var other uuid.UUID
	if err := f.pool.QueryRow(f.ctx, "INSERT INTO projects (name) VALUES ('other') RETURNING id").Scan(&other); err != nil {
		t.Fatal(err)
	}
	fo := f.fanout(t, nil, nil)
	for _, project := range []uuid.UUID{other, uuid.New()} {
		args := jobs.BroadcastFanoutArgs{BroadcastID: b.ID, ProjectID: project}
		if err := fo.Run(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
			t.Fatalf("foreign project: %v (want the job to end)", err)
		}
	}
	if n := f.count(t, "SELECT count(*) FROM emails"); n != 0 {
		t.Fatalf("emails from a job with a foreign project: %d", n)
	}
	if got, err := f.store.Get(f.ctx, f.project, b.ID); err != nil || got.Status != broadcasts.StatusScheduled || got.StartedAt != nil {
		t.Fatalf("broadcast changed by a foreign job: %+v %v", got, err)
	}
}
