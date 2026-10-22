//go:build integration

package retention

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

const day = 24 * time.Hour

type fixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	project uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.NewMigrated(t)
	p, err := auth.CreateProject(context.Background(), store.New(pool), "app")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, pool: pool, project: p.ID}
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) id(sql string, args ...any) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// email inserts a finished (captured) email, or an unfinished (sending) one
// when finished is the zero time.
func (f *fixture) email(recipient string, finished time.Time) uuid.UUID {
	f.t.Helper()
	if finished.IsZero() {
		return f.id(`INSERT INTO emails (project_id, sender, recipient, subject, status, created_at)
			VALUES ($1, 'app@example.test', $2, 'Hi', 'sending', now() - interval '40 days') RETURNING id`, f.project, recipient)
	}
	return f.id(`INSERT INTO emails (project_id, sender, recipient, subject, status, provider, created_at, finished_at)
		VALUES ($1, 'app@example.test', $2, 'Hi', 'captured', 'mailpit', $3, $3) RETURNING id`, f.project, recipient, finished)
}

func (f *fixture) event(emailID uuid.UUID, received time.Time) uuid.UUID {
	f.t.Helper()
	return f.id(`INSERT INTO email_events (project_id, email_id, origin, type, occurred_at, received_at)
		VALUES ($1, $2, 'platform', 'captured', $3, $3) RETURNING id`, f.project, emailID, received)
}

func (f *fixture) endpoint() uuid.UUID {
	f.t.Helper()
	return f.id(`INSERT INTO webhook_endpoints (id, project_id, url, event_types, secret_ciphertext, secret_key_id)
		VALUES ($1, $2, 'https://hooks.example.test/', '{email.captured}', $3, 'k1') RETURNING id`,
		uuid.New(), f.project, make([]byte, 64))
}

// delivery inserts a delivery in status (pending, delivered or failed) last
// changed at updated.
func (f *fixture) delivery(eventID uuid.UUID, status string, updated time.Time) uuid.UUID {
	f.t.Helper()
	return f.id(`INSERT INTO webhook_deliveries (project_id, endpoint_id, event_id, status, delivered_at, last_error_code, created_at, updated_at)
		VALUES ($1, $2, $3, $4,
		        CASE WHEN $4 = 'delivered' THEN $5::timestamptz END,
		        CASE WHEN $4 = 'failed' THEN 'http_500' END, $5, $5) RETURNING id`,
		f.project, f.endpoint(), eventID, status, updated)
}

func (f *fixture) exists(table string, id uuid.UUID) bool {
	f.t.Helper()
	var ok bool
	if err := f.pool.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1)", id).Scan(&ok); err != nil {
		f.t.Fatal(err)
	}
	return ok
}

func (f *fixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) pruner(logs *bytes.Buffer, emailRetention time.Duration) Pruner {
	return Pruner{
		Pool: f.pool, Logger: slog.New(slog.NewJSONHandler(logs, nil)),
		EventRetention: 90 * day, DeliveryRetention: 30 * day, EmailRetention: emailRetention, ContactEventRetention: 90 * day,
	}
}

func TestPrune(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := time.Now()
	old, recent := now.Add(-100*day), now.Add(-day)

	finished := f.email("a@example.test", now.Add(-101*day))
	sending := f.email("b@example.test", time.Time{})

	// Events.
	oldEvent := f.event(finished, old)
	itsDelivery := f.delivery(oldEvent, "delivered", recent)
	heldEvent := f.event(finished, old)
	pendingDelivery := f.delivery(heldEvent, "pending", old)
	unfinishedEvent := f.event(sending, old)
	recentEvent := f.event(finished, recent)

	// Deliveries of the recent event.
	oldDelivered := f.delivery(recentEvent, "delivered", now.Add(-31*day))
	oldFailed := f.delivery(recentEvent, "failed", now.Add(-31*day))
	oldPending := f.delivery(recentEvent, "pending", now.Add(-31*day))
	recentDelivered := f.delivery(recentEvent, "delivered", now.Add(-29*day))

	// Admin sessions.
	user := f.id(`INSERT INTO admin_users (username, password_hash) VALUES ('ops', '$argon2id$x') RETURNING id`)
	session := func(created, lastSeen, expires time.Time) uuid.UUID {
		hash := make([]byte, 32)
		_, _ = rand.Read(hash)
		return f.id(`INSERT INTO admin_sessions (admin_user_id, token_hash, created_at, last_seen_at, expires_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, user, hash, created, lastSeen, expires)
	}
	expired := session(now.Add(-13*time.Hour), now.Add(-2*time.Minute), now.Add(-time.Hour))
	idle := session(now.Add(-26*time.Hour), now.Add(-25*time.Hour), now.Add(time.Hour))
	quiet := session(now.Add(-2*time.Hour), now.Add(-time.Hour), now.Add(time.Hour))
	live := session(now.Add(-time.Minute), now, now.Add(12*time.Hour))

	var logs bytes.Buffer
	if err := f.pruner(&logs, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		table string
		id    uuid.UUID
		want  bool
	}{
		"old event of a finished email":          {"email_events", oldEvent, false},
		"its delivery (cascade)":                 {"webhook_deliveries", itsDelivery, false},
		"old event with a pending delivery":      {"email_events", heldEvent, true},
		"that pending delivery":                  {"webhook_deliveries", pendingDelivery, true},
		"old event of an unfinished email":       {"email_events", unfinishedEvent, true},
		"recent event":                           {"email_events", recentEvent, true},
		"old delivered delivery":                 {"webhook_deliveries", oldDelivered, false},
		"old failed delivery":                    {"webhook_deliveries", oldFailed, false},
		"old pending delivery":                   {"webhook_deliveries", oldPending, true},
		"recent delivered delivery":              {"webhook_deliveries", recentDelivered, true},
		"email (email retention off)":            {"emails", finished, true},
		"session past its absolute expiry":       {"admin_sessions", expired, false},
		"session idle for more than 24h":         {"admin_sessions", idle, false},
		"session idle for an hour":               {"admin_sessions", quiet, true},
		"live session":                           {"admin_sessions", live, true},
		"unfinished email (email retention off)": {"emails", sending, true},
	} {
		if got := f.exists(c.table, c.id); got != c.want {
			t.Errorf("%s: exists = %v, want %v", name, got, c.want)
		}
	}
	for _, want := range []string{`"events_deleted":1`, `"deliveries_deleted":2`, `"sessions_deleted":2`, `"emails_deleted":0`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %s: %s", want, logs.String())
		}
	}

	// A second run finds nothing and logs nothing.
	logs.Reset()
	if err := f.pruner(&logs, 0).Run(context.Background()); err != nil || logs.Len() != 0 {
		t.Fatalf("second run: err = %v, logs = %s", err, logs.String())
	}
}

func TestPruneEmails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := time.Now()
	finishedAt := now.Add(-40 * day)

	old := f.email("old-recipient@example.test", finishedAt)
	f.exec(`INSERT INTO email_contents (email_id, project_id, text_body) VALUES ($1, $2, 'body')`, old, f.project)
	f.exec(`INSERT INTO idempotency_keys (project_id, operation, key, fingerprint, email_id) VALUES ($1, 'create_email', 'k', $2, $3)`,
		f.project, make([]byte, 32), old)
	oldEvent := f.event(old, finishedAt)
	// Recent, so only the cascade from the email can remove it.
	oldDelivery := f.delivery(oldEvent, "delivered", now.Add(-day))

	held := f.email("held@example.test", finishedAt)
	heldDelivery := f.delivery(f.event(held, finishedAt), "pending", finishedAt)
	unfinished := f.email("open@example.test", time.Time{})
	recent := f.email("recent@example.test", now.Add(-10*day))
	f.exec(`INSERT INTO suppressions (project_id, recipient, reason, created_at) VALUES ($1, 'old-recipient@example.test', 'bounce', $2)`,
		f.project, finishedAt)

	// Off by default: nothing is deleted however old.
	var logs bytes.Buffer
	if err := f.pruner(&logs, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.exists("emails", old) || !f.exists("webhook_deliveries", oldDelivery) {
		t.Fatal("email or its delivery deleted although email retention is off")
	}

	logs.Reset()
	if err := f.pruner(&logs, 30*day).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		table string
		id    uuid.UUID
		want  bool
	}{
		"old finished email":                {"emails", old, false},
		"its event":                         {"email_events", oldEvent, false},
		"its delivery":                      {"webhook_deliveries", oldDelivery, false},
		"old email with a pending delivery": {"emails", held, true},
		"that pending delivery":             {"webhook_deliveries", heldDelivery, true},
		"unfinished email":                  {"emails", unfinished, true},
		"recently finished email":           {"emails", recent, true},
	} {
		if got := f.exists(c.table, c.id); got != c.want {
			t.Errorf("%s: exists = %v, want %v", name, got, c.want)
		}
	}
	if n := f.count(`SELECT count(*) FROM email_contents WHERE email_id = $1`, old); n != 0 {
		t.Errorf("content of the deleted email survives (%d rows)", n)
	}
	if n := f.count(`SELECT count(*) FROM idempotency_keys WHERE email_id = $1`, old); n != 0 {
		t.Errorf("idempotency key of the deleted email survives (%d rows)", n)
	}
	if n := f.count(`SELECT count(*) FROM suppressions WHERE project_id = $1`, f.project); n != 1 {
		t.Errorf("suppressions = %d, want 1 (never deleted by retention)", n)
	}
	l := logs.String()
	if !strings.Contains(l, `"emails_deleted":1`) {
		t.Errorf("log lacks the email count: %s", l)
	}
	for _, leak := range []string{"old-recipient", old.String(), f.project.String()} {
		if strings.Contains(l, leak) {
			t.Errorf("log contains %q: %s", leak, l)
		}
	}
}

func TestPruneBatches(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	email := f.email("a@example.test", time.Now().Add(-200*day))
	f.exec(`INSERT INTO email_events (project_id, email_id, origin, type, occurred_at, received_at)
		SELECT $1, $2, 'platform', 'captured', now() - interval '100 days', now() - interval '100 days'
		FROM generate_series(1, 2500)`, f.project, email)

	var logs bytes.Buffer
	if err := f.pruner(&logs, 0).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM email_events`); n != 0 {
		t.Fatalf("events left = %d, want 0", n)
	}
	if !strings.Contains(logs.String(), `"events_deleted":2500`) {
		t.Fatalf("log: %s", logs.String())
	}

	// A cancelled run stops and reports the cancellation.
	f.exec(`INSERT INTO email_events (project_id, email_id, origin, type, occurred_at, received_at)
		SELECT $1, $2, 'platform', 'captured', now() - interval '100 days', now() - interval '100 days'
		FROM generate_series(1, 10)`, f.project, email)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.pruner(&logs, 0).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run: err = %v, want context.Canceled", err)
	}
	if n := f.count(`SELECT count(*) FROM email_events`); n != 10 {
		t.Fatalf("cancelled run deleted events: %d left, want 10", n)
	}
}

// An event ingest holding an old email's row lock while it adds a pending
// delivery: the email must survive, its delivery too.
func TestPruneEmailsSkipsEmailsBeingUpdated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	email := f.email("busy@example.test", time.Now().Add(-40*day))

	ingest, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ingest.Rollback(context.Background()) })
	if _, err := ingest.Exec(ctx, `SELECT 1 FROM emails WHERE id = $1 FOR UPDATE`, email); err != nil {
		t.Fatal(err)
	}
	var eventID uuid.UUID
	if err := ingest.QueryRow(ctx, `INSERT INTO email_events (project_id, email_id, origin, type, occurred_at)
		VALUES ($1, $2, 'platform', 'captured', now()) RETURNING id`, f.project, email).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	endpoint := f.endpoint()
	if _, err := ingest.Exec(ctx, `INSERT INTO webhook_deliveries (project_id, endpoint_id, event_id) VALUES ($1, $2, $3)`,
		f.project, endpoint, eventID); err != nil {
		t.Fatal(err)
	}

	// The run must skip the locked email instead of waiting for it.
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var logs bytes.Buffer
	if err := f.pruner(&logs, 30*day).Run(runCtx); err != nil {
		t.Fatalf("run while the email is locked: %v (it must skip the email, not wait)", err)
	}
	if err := ingest.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.exists("emails", email) || !f.exists("email_events", eventID) {
		t.Fatal("email deleted while an ingest was adding a pending delivery")
	}
	if n := f.count(`SELECT count(*) FROM webhook_deliveries WHERE event_id = $1 AND status = 'pending'`, eventID); n != 1 {
		t.Fatalf("pending deliveries = %d, want 1", n)
	}
}

// One kind failing does not stop the others.
func TestPruneContinuesAfterAFailingKind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	now := time.Now()
	email := f.email("a@example.test", now.Add(-200*day))
	event := f.event(email, now.Add(-100*day))
	f.delivery(f.event(email, now), "delivered", now.Add(-40*day))
	user := f.id(`INSERT INTO admin_users (username, password_hash) VALUES ('ops', '$argon2id$x') RETURNING id`)
	session := f.id(`INSERT INTO admin_sessions (admin_user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, now() - interval '2 days', now() - interval '2 days', now() - interval '1 day') RETURNING id`,
		user, make([]byte, 32))
	f.exec(`CREATE FUNCTION refuse() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`)
	f.exec(`CREATE TRIGGER refuse_delivery_delete BEFORE DELETE ON webhook_deliveries FOR EACH ROW EXECUTE FUNCTION refuse()`)

	var logs bytes.Buffer
	err := f.pruner(&logs, 0).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "webhook deliveries") {
		t.Fatalf("err = %v, want the delivery failure", err)
	}
	if f.exists("email_events", event) || f.exists("admin_sessions", session) {
		t.Fatal("a failing delivery delete stopped event or session retention")
	}
}

// A Pruner with retention below the minimums deletes nothing.
func TestPruneRefusesShortRetention(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	email := f.email("a@example.test", time.Now().Add(-2*day))
	event := f.event(email, time.Now().Add(-day))
	for _, p := range []Pruner{
		{Pool: f.pool, Logger: slog.New(slog.DiscardHandler), DeliveryRetention: 30 * day},
		{Pool: f.pool, Logger: slog.New(slog.DiscardHandler), EventRetention: 90 * day},
		{Pool: f.pool, Logger: slog.New(slog.DiscardHandler), EventRetention: 90 * day, DeliveryRetention: 30 * day, EmailRetention: day, ContactEventRetention: 90 * day},
		{Pool: f.pool, Logger: slog.New(slog.DiscardHandler), EventRetention: 90 * day, DeliveryRetention: 30 * day, ContactEventRetention: day - time.Second},
	} {
		if err := p.Run(context.Background()); err == nil {
			t.Errorf("%+v: no error", p)
		}
	}
	if !f.exists("email_events", event) || !f.exists("emails", email) {
		t.Fatal("a misconfigured run deleted data")
	}
}

// The second step re-checks: an email whose pending delivery was committed
// after the first step read is not deleted.
func TestDeleteLockedEmailsRechecksPendingDeliveries(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	email := f.email("late@example.test", time.Now().Add(-40*day))
	delivery := f.delivery(f.event(email, time.Now()), "pending", time.Now())
	n, err := store.New(f.pool).DeleteLockedEmails(ctx, []uuid.UUID{email})
	if err != nil || n != 0 {
		t.Fatalf("deleted = %d, %v; want 0", n, err)
	}
	if !f.exists("emails", email) || !f.exists("webhook_deliveries", delivery) {
		t.Fatal("email with a pending delivery deleted")
	}
	plain := f.email("plain@example.test", time.Now().Add(-40*day))
	if n, err := store.New(f.pool).DeleteLockedEmails(ctx, []uuid.UUID{plain}); err != nil || n != 1 {
		t.Fatalf("deleted = %d, %v; want 1", n, err)
	}
}
