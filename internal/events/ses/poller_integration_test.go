//go:build integration

package ses

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/testaws"
	"github.com/skraus2/email-platform/internal/testdb"
)

// pollerDB is a migrated database with the fixture email submitted through
// SES, and a recorder that counts status changes.
type pollerDB struct {
	pool    *pgxpool.Pool
	rec     *events.Recorder
	mu      sync.Mutex
	changes []string
}

func newPollerDB(t *testing.T) *pollerDB {
	t.Helper()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	if _, err := pool.Exec(ctx, "INSERT INTO projects (id, name) VALUES ($1, 'app')", fixtureProject); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO emails (id, project_id, sender, recipient, subject, status, provider, provider_id, finished_at)
		VALUES ($1, $2, 'noreply@example.test', 'customer@example.test', 'Hi', 'submitted', 'ses', $3, now())`,
		fixtureEmail, fixtureProject, fixtureMessageID); err != nil {
		t.Fatal(err)
	}
	db := &pollerDB{pool: pool, rec: events.NewRecorder(pool, testLogger(t))}
	db.rec.OnStatusChange(func(_ context.Context, _ pgx.Tx, c events.Change) error {
		db.mu.Lock()
		defer db.mu.Unlock()
		db.changes = append(db.changes, c.Status)
		return nil
	})
	return db
}

func testLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(t.Output(), nil))
}

func (db *pollerDB) state(t *testing.T) (status string, nEvents, nSuppressed int, changes []string) {
	t.Helper()
	ctx := context.Background()
	if err := db.pool.QueryRow(ctx, "SELECT status FROM emails WHERE id = $1", fixtureEmail).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, "SELECT count(*) FROM email_events WHERE email_id = $1", fixtureEmail).Scan(&nEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.pool.QueryRow(ctx, "SELECT count(*) FROM suppressions WHERE project_id = $1", fixtureProject).Scan(&nSuppressed); err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	return status, nEvents, nSuppressed, append([]string(nil), db.changes...)
}

// AC14: queue → authenticate → parse → ingest → delete, against the real
// event store.
func TestPollerIngestsThroughQueue(t *testing.T) {
	t.Parallel()
	db := newPollerDB(t)
	ps := newTestPoller(t, nil)
	ps.poller.ingest = db.rec.Ingest
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	ps.stub.Add(ps.signed(t, fixture(t, "bounce_permanent")))
	ps.stub.Add(ps.signed(t, fixture(t, "open"))) // ignored, never stored
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, n, suppressed, changes := db.state(t)
	if status != "bounced" || n != 2 || suppressed != 1 || strings.Join(changes, ",") != "delivered,bounced" {
		t.Fatalf("status %s, %d events, %d suppressions, changes %v", status, n, suppressed, changes)
	}
	if ps.stub.Pending() != 0 || len(ps.stub.Deleted()) != 3 {
		t.Fatalf("pending %d, deleted %d", ps.stub.Pending(), len(ps.stub.Deleted()))
	}
}

// A message whose delete failed comes back and is stored only once.
func TestPollerRedeliveryIsDeduplicated(t *testing.T) {
	t.Parallel()
	db := newPollerDB(t)
	ps := newTestPoller(t, nil)
	ps.poller.ingest = db.rec.Ingest
	ps.stub.Fail(func(r testaws.SQSRequest) testaws.Response {
		if r.Operation == "DeleteMessage" {
			return testaws.Response{Status: http.StatusInternalServerError, ErrorType: "InternalError"}
		}
		return testaws.Response{}
	})
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ps.stub.Pending() != 1 {
		t.Fatal("message deleted despite the failing delete")
	}
	ps.stub.Fail(nil)
	ps.stub.ExpireVisibility()
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, n, _, changes := db.state(t)
	if status != "delivered" || n != 1 || len(changes) != 1 {
		t.Fatalf("status %s, %d events, changes %v; want delivered, 1, one change", status, n, changes)
	}
	if ps.stub.Pending() != 0 || !strings.Contains(ps.logs.String(), `"outcome":"duplicate"`) {
		t.Fatalf("pending %d, logs:\n%s", ps.stub.Pending(), ps.logs)
	}
}

// A database error leaves the message on the queue; the redelivery is
// ingested once the database is back.
func TestPollerKeepsMessageWhenDatabaseFails(t *testing.T) {
	t.Parallel()
	db := newPollerDB(t)
	down, err := pgxpool.New(context.Background(), db.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	down.Close()
	ps := newTestPoller(t, nil)
	ps.poller.ingest = events.NewRecorder(down, testLogger(t)).Ingest
	ps.stub.Add(ps.signed(t, fixture(t, "delivery")))
	if err := ps.poller.poll(context.Background()); err == nil {
		t.Fatal("poll reported no error")
	}
	if ps.stub.Pending() != 1 || len(ps.stub.Deleted()) != 0 {
		t.Fatalf("pending %d, deleted %d; want the message kept", ps.stub.Pending(), len(ps.stub.Deleted()))
	}

	ps.poller.ingest = db.rec.Ingest
	ps.stub.ExpireVisibility()
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, n, _, _ := db.state(t); status != "delivered" || n != 1 || ps.stub.Pending() != 0 {
		t.Fatalf("status %s, %d events, pending %d", status, n, ps.stub.Pending())
	}
}

// Events for an email of another project (or none) are deleted, nothing is
// stored.
func TestPollerDropsUnknownEmails(t *testing.T) {
	t.Parallel()
	db := newPollerDB(t)
	ps := newTestPoller(t, nil)
	ps.poller.ingest = db.rec.Ingest
	foreign := strings.Replace(fixture(t, "delivery"), fixtureProject.String(), "0199e39f-0000-7000-8000-000000000002", 1)
	ps.stub.Add(ps.signed(t, foreign))
	if err := ps.poller.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, n, _, _ := db.state(t); status != "submitted" || n != 0 || ps.stub.Pending() != 0 {
		t.Fatalf("status %s, %d events, pending %d", status, n, ps.stub.Pending())
	}
	if !strings.Contains(ps.logs.String(), `"outcome":"unknown_email"`) {
		t.Fatalf("logs:\n%s", ps.logs)
	}
}
