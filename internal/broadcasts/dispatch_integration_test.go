//go:build integration

package broadcasts_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/broadcasts"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
)

type recordingProvider struct {
	mu   sync.Mutex
	sent []delivery.Message
}

func (p *recordingProvider) Send(_ context.Context, m delivery.Message) (delivery.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, m)
	return delivery.Result{Provider: "mailpit", ProviderID: "<" + m.ID.String() + ">", Status: delivery.StatusCaptured}, nil
}

// fannedOut runs the fan-out of a scheduled broadcast over the standard
// members and returns the recipient email ids by address.
func (f *fixture) fannedOut(t *testing.T) (broadcasts.Broadcast, map[string]uuid.UUID, audienceSetup) {
	t.Helper()
	m := f.members(t)
	b := f.scheduled(t)
	runAll(t, f.fanout(t, nil, nil), b)
	rows, err := f.pool.Query(f.ctx, "SELECT recipient, id FROM emails WHERE broadcast_id = $1", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]uuid.UUID{}
	for rows.Next() {
		var r string
		var id uuid.UUID
		if err := rows.Scan(&r, &id); err != nil {
			t.Fatal(err)
		}
		ids[r] = id
	}
	return b, ids, m
}

func (f *fixture) dispatch(t *testing.T, p delivery.Provider, id uuid.UUID) {
	t.Helper()
	d := emails.NewDispatcher(f.pool, p, slog.New(slog.DiscardHandler), 5*time.Second, time.Hour)
	if err := d.SendEmail(f.ctx, jobs.SendEmailArgs{EmailID: id, ProjectID: f.project}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) state(t *testing.T, id uuid.UUID) (string, *string) {
	t.Helper()
	var st string
	var code *string
	if err := f.pool.QueryRow(f.ctx, "SELECT status, error_code FROM emails WHERE id = $1", id).Scan(&st, &code); err != nil {
		t.Fatal(err)
	}
	return st, code
}

func (f *fixture) token(t *testing.T, contactID uuid.UUID) string {
	t.Helper()
	var tok string
	if err := f.pool.QueryRow(f.ctx, "SELECT unsubscribe_token FROM contacts WHERE id = $1", contactID).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestBroadcastEmailsRenderPerContactAtSend(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	_, ids, m := f.fannedOut(t)
	// The contact's attributes at send time count.
	if _, err := contacts.NewStore(f.pool, 1000).Update(f.ctx, f.project, m.ids["bob@example.test"], contacts.UpdateInput{
		Attributes: []byte(`{"name":"Robert"}`)}); err != nil {
		t.Fatal(err)
	}
	p := &recordingProvider{}
	for _, addr := range []string{"ada@example.test", "bob@example.test"} {
		f.dispatch(t, p, ids[addr])
		if st, code := f.state(t, ids[addr]); st != "captured" || code != nil {
			t.Fatalf("%s: %s %v", addr, st, code)
		}
	}
	if len(p.sent) != 2 {
		t.Fatalf("sent %d", len(p.sent))
	}
	ada, bob := p.sent[0], p.sent[1]
	adaURL := "https://app.example.test/unsubscribe?t=" + f.token(t, m.ids["ada@example.test"])
	if ada.To != "ada@example.test" || ada.Subject != "News for ADA (own)" || ada.Text != "Hi ADA "+adaURL || ada.ListUnsubscribeURL != adaURL || ada.HTML != "" {
		t.Fatalf("ada: %+v", ada)
	}
	// Stored subject (fan-out time), body from the current attributes.
	if bob.Subject != "News for BOB (own)" || !strings.HasPrefix(bob.Text, "Hi Robert https://app.example.test/unsubscribe?t=") ||
		strings.Contains(bob.Text, f.token(t, m.ids["ada@example.test"])) {
		t.Fatalf("bob: %+v", bob)
	}
	if n := f.count(t, "SELECT count(*) FROM email_contents"); n != 0 {
		t.Fatalf("contents: %d", n)
	}
}

func TestBroadcastEmailRechecksAtSend(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	b, ids, m := f.fannedOut(t)
	cs := contacts.NewStore(f.pool, 1000)
	// ada unsubscribes through her link, bob is deleted, cy's attributes no
	// longer render, dee's address bounces.
	if _, err := cs.Unsubscribe(f.ctx, f.project, f.token(t, m.ids["ada@example.test"])); err != nil {
		t.Fatal(err)
	}
	if err := cs.Delete(f.ctx, f.project, m.ids["bob@example.test"]); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Update(f.ctx, f.project, m.ids["cy@example.test"], contacts.UpdateInput{Attributes: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, "INSERT INTO suppressions (project_id, recipient, reason) VALUES ($1, 'dee@example.test', 'complaint')", f.project); err != nil {
		t.Fatal(err)
	}
	p := &recordingProvider{}
	want := map[string]string{
		"ada@example.test": emails.CodeUnsubscribed, "bob@example.test": emails.CodeContactDeleted,
		"cy@example.test": emails.CodeRenderFailed, "dee@example.test": emails.CodeSuppressed,
	}
	for addr, code := range want {
		f.dispatch(t, p, ids[addr])
		if st, got := f.state(t, ids[addr]); st != "failed" || got == nil || *got != code {
			t.Errorf("%s: %s %v, want failed %s", addr, st, got, code)
		}
	}
	if len(p.sent) != 0 {
		t.Fatalf("sent %d messages", len(p.sent))
	}
	// The deleted contact's email keeps its recipient, contact_id is gone.
	var cid *uuid.UUID
	if err := f.pool.QueryRow(f.ctx, "SELECT contact_id FROM emails WHERE id = $1", ids["bob@example.test"]).Scan(&cid); err != nil || cid != nil {
		t.Fatalf("contact_id after delete: %v %v", cid, err)
	}
	_ = b
}

func TestCanceledBroadcastStopsQueuedEmails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 20)
	b, ids, _ := f.fannedOut(t)
	// One email was claimed by an attempt that may have reached the provider.
	if _, err := f.pool.Exec(f.ctx, "UPDATE emails SET status = 'sending', attempts = 1 WHERE id = $1", ids["dee@example.test"]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Cancel(f.ctx, f.project, b.ID); err != nil {
		t.Fatal(err)
	}
	p := &recordingProvider{}
	for _, id := range ids {
		f.dispatch(t, p, id)
	}
	if len(p.sent) != 0 {
		t.Fatalf("sent %d after cancel", len(p.sent))
	}
	for addr, id := range ids {
		st, code := f.state(t, id)
		if addr == "dee@example.test" {
			if st != "failed" || code == nil || *code != emails.CodeBroadcastCanceled {
				t.Errorf("sending email: %s %v", st, code)
			}
			continue
		}
		if st != "canceled" || code != nil {
			t.Errorf("%s: %s %v", addr, st, code)
		}
	}
	if n := f.count(t, "SELECT count(*) FROM email_events WHERE type = 'canceled'"); n != 3 {
		t.Fatalf("canceled events: %d", n)
	}
	got, _ := f.store.Get(f.ctx, f.project, b.ID)
	if got.Counts.ByStatus["canceled"] != 3 || got.Counts.ByStatus["failed"] != 1 {
		t.Fatalf("counts: %+v", got.Counts.ByStatus)
	}
}
