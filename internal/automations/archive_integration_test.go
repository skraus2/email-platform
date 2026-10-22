//go:build integration

package automations_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/jobs"
)

// seedRuns inserts n runs of the automation in the project (one contact
// each) with the given status, directly in SQL.
func (f *fixture) seedRuns(t *testing.T, project, automation uuid.UUID, n int, status string) {
	t.Helper()
	reason, finished, next := "NULL", "NULL", "now() + interval '1 day'"
	if status != automations.RunActive {
		reason, finished, next = "'condition_not_met'", "now()", "NULL"
	}
	_, err := f.pool.Exec(f.ctx, `
		WITH c AS (
			INSERT INTO contacts (project_id, email, status, consent_source, unsubscribe_token)
			SELECT $1, 'c' || g || '-' || $4::text || '@example.test', 'subscribed', 'test',
			       substr(md5(random()::text) || md5(random()::text), 1, 43)
			FROM generate_series(1, $3) g
			RETURNING id)
		INSERT INTO automation_runs (project_id, automation_id, contact_id, status, reason, next_step_at, finished_at)
		SELECT $1, $2, c.id, $5, `+reason+`, `+next+`, `+finished+` FROM c`,
		project, automation, n, uuid.NewString()[:8], status)
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchiveCancelsRunsInBatches(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	a, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	keep, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	f.seedRuns(t, f.project, a.ID, automations.ArchiveBatch+5, automations.RunActive)
	f.seedRuns(t, f.project, a.ID, 3, automations.RunExited)
	f.seedRuns(t, f.project, keep.ID, 4, automations.RunActive)

	handler := automations.CancelArchivedRuns(f.pool)
	args := jobs.AutomationArchiveArgs{AutomationID: a.ID, ProjectID: f.project}
	// Not archived: nothing happens.
	if err := handler(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatalf("handler before archive: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE status = 'canceled'`); n != 0 {
		t.Fatalf("%d runs canceled before archive", n)
	}
	// The same automation id under another project is not touched.
	other := jobs.AutomationArchiveArgs{AutomationID: a.ID, ProjectID: f.other}

	archived := automations.StatusArchived
	if _, err := f.store.Update(f.ctx, f.project, a.ID, automations.UpdateInput{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	if err := handler(f.ctx, other, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatalf("foreign project: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE status = 'canceled'`); n != 0 {
		t.Fatalf("foreign project canceled %d runs", n)
	}

	// A full batch snoozes for the next one.
	err = handler(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25})
	if _, ok := jobs.SnoozeDelay(err); !ok {
		t.Fatalf("full batch: want snooze, got %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'canceled'`, a.ID); n != automations.ArchiveBatch {
		t.Fatalf("first batch canceled %d", n)
	}

	// A run locked by another transaction is skipped and retried later.
	tx, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var locked uuid.UUID
	if err := tx.QueryRow(f.ctx, `SELECT id FROM automation_runs WHERE automation_id = $1 AND status = 'active' LIMIT 1 FOR NO KEY UPDATE`, a.ID).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	err = handler(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25})
	if d, ok := jobs.SnoozeDelay(err); !ok || d < time.Second {
		t.Fatalf("locked run left: want a retry snooze, got %v", err)
	}
	if err := tx.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := handler(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatalf("last pass: %v", err)
	}

	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'canceled'
		AND reason = 'automation_archived' AND finished_at IS NOT NULL AND next_step_at IS NULL AND trigger_data IS NULL`, a.ID); n != automations.ArchiveBatch+5 {
		t.Fatalf("canceled %d, want %d", n, automations.ArchiveBatch+5)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'exited'`, a.ID); n != 3 {
		t.Fatalf("ended runs changed: %d exited", n)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND status = 'active'`, keep.ID); n != 4 {
		t.Fatalf("other automation's runs changed: %d active", n)
	}
	got, err := f.store.Get(f.ctx, f.project, a.ID)
	if err != nil || got.RunCounts[automations.RunCanceled] != automations.ArchiveBatch+5 || got.RunCounts[automations.RunExited] != 3 {
		t.Fatalf("run counts: %v %v", got.RunCounts, err)
	}
}

func TestRunStateConstraints(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	a, err := f.store.Create(f.ctx, f.project, f.input())
	if err != nil {
		t.Fatal(err)
	}
	f.seedRuns(t, f.project, a.ID, 1, automations.RunActive)
	var run uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT id FROM automation_runs`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"exited without reason":    `status = 'exited', reason = NULL, next_step_at = NULL, finished_at = now()`,
		"failed with wrong reason": `status = 'failed', reason = 'canceled', next_step_at = NULL, finished_at = now()`,
		"completed with reason":    `status = 'completed', reason = 'canceled', next_step_at = NULL, finished_at = now()`,
		"ended but due":            `status = 'canceled', reason = 'canceled', finished_at = now()`,
		"ended keeps data":         `status = 'canceled', reason = 'canceled', next_step_at = NULL, finished_at = now(), trigger_data = '{}'`,
		"active finished":          `finished_at = now()`,
		"step beyond limit":        `step = 21`,
	}
	for name, set := range bad {
		if _, err := f.pool.Exec(f.ctx, `UPDATE automation_runs SET `+set+` WHERE id = $1`, run); err == nil || !strings.Contains(err.Error(), "check") {
			t.Errorf("%s: want a check violation, got %v", name, err)
		}
	}
	// A second active run of the same automation and contact is refused.
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO automation_runs (project_id, automation_id, contact_id, next_step_at)
		SELECT project_id, automation_id, contact_id, now() FROM automation_runs WHERE id = $1`, run); err == nil {
		t.Error("second active run accepted")
	}
	// A run never references a contact of another project.
	c, err := contacts.NewStore(f.pool, 10).Create(f.ctx, f.other, contacts.CreateInput{Email: "x@example.test", Status: contacts.StatusUnsubscribed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO automation_runs (project_id, automation_id, contact_id, next_step_at) VALUES ($1, $2, $3, now())`,
		f.project, a.ID, c.ID); err == nil {
		t.Error("run with a foreign contact accepted")
	}
}

func TestDefinitionSizeLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	// A template with a long declared variable lets shared variables grow
	// to near their 16 KiB limit per send step.
	tmpl, err := f.templates.Create(f.ctx, f.project, "big", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.templates.Publish(f.ctx, f.project, tmpl.ID, goodSource,
		[]byte(`{"unsubscribe_url":{"type":"string","required":true},"name":{"type":"string","max_length":10000},"order":{"type":"string"}}`)); err != nil {
		t.Fatal(err)
	}
	send := func(n int) string {
		vars, _ := json.Marshal(map[string]string{"name": strings.Repeat("x", n)})
		return strings.Replace(f.sendStep(tmpl.ID, "1"), `{"plan":"pro"}`, string(vars), 1)
	}
	steps := func(k, n int) []string {
		out := make([]string, k)
		for i := range out {
			out[i] = send(n)
		}
		return out
	}
	_, err = f.store.Create(f.ctx, f.project, f.input(steps(8, 9000)...))
	var e *automations.Error
	if !errors.As(err, &e) || e.Field != "steps" {
		t.Fatalf("over 64 KiB: %v", err)
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input(steps(6, 9000)...)); err != nil {
		t.Fatalf("under 64 KiB: %v", err)
	}
}

func TestAutomationCreateRate(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	f.store.LimitCreates(2)
	archived := automations.StatusArchived
	for range 2 {
		a, err := f.store.Create(f.ctx, f.project, f.input())
		if err != nil {
			t.Fatal(err)
		}
		// Archived automations count: create + archive cannot loop.
		if _, err := f.store.Update(f.ctx, f.project, a.ID, automations.UpdateInput{Status: &archived}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input()); !errors.Is(err, automations.ErrRateLimited) {
		t.Fatalf("third create in the hour: %v", err)
	}
	// Refused before any template work: an unknown template is not reported.
	if _, err := f.store.Create(f.ctx, f.project, f.input(f.sendStep(uuid.New(), "1"))); !errors.Is(err, automations.ErrRateLimited) {
		t.Fatalf("rate before template checks: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE automations SET created_at = now() - interval '61 minutes'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Create(f.ctx, f.project, f.input()); err != nil {
		t.Fatalf("after the hour: %v", err)
	}
}
