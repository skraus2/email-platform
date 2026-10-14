//go:build integration

package ses

import (
	"context"
	"log/slog"
	"testing"

	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/testdb"
)

// Every tracked fixture passes the whole chain: SNS verification, parsing
// and the ingest's own validation; the projection ends in the right status.
func TestFixturesIngestEndToEnd(t *testing.T) {
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
	rec := events.NewRecorder(pool, slog.New(slog.DiscardHandler))
	s := validSigner(t)
	v := s.verifier(nil)
	seen := map[string]bool{}
	for _, name := range []string{"send", "delivery", "delivery_delay", "bounce_transient", "bounce_permanent", "complaint", "reject", "delivery"} {
		msg, err := v.Verify(ctx, s.envelope(t, fixture(t, name), nil, nil))
		if err != nil {
			t.Fatalf("%s: verify: %v", name, err)
		}
		ev, err := Parse(msg)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		out, err := rec.Ingest(ctx, ev)
		if err != nil {
			t.Fatalf("%s: ingest: %v", name, err)
		}
		want := events.Stored
		if seen[name] {
			want = events.Duplicate // the same notification delivered again
		}
		seen[name] = true
		if out != want {
			t.Fatalf("%s: outcome %v", name, out)
		}
	}
	var status string
	var n, suppressed int
	if err := pool.QueryRow(ctx, "SELECT status FROM emails WHERE id = $1", fixtureEmail).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM email_events WHERE email_id = $1", fixtureEmail).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM suppressions WHERE project_id = $1", fixtureProject).Scan(&suppressed); err != nil {
		t.Fatal(err)
	}
	if status != "complained" || n != 7 || suppressed != 1 {
		t.Fatalf("status %s, %d events, %d suppressions; want complained, 7, 1", status, n, suppressed)
	}
}
