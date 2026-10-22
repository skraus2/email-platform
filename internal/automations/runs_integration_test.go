//go:build integration

package automations_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/automations"
)

func TestRunsGetListCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	runs := automations.NewRuns(f.pool)
	run, contact := f.startRun(t, `{"order":"A-1"}`, f.sendStep(f.template, "1"), wait60, f.sendStep(f.template, "1"))
	f.step(t, e, run, 0)

	got, err := runs.Get(f.ctx, f.project, run)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != automations.RunActive || got.Step != 2 || got.NextStepAt == nil || got.ContactID != contact || got.Reason != nil ||
		len(got.Emails) != 1 || got.Emails[0].Step != 0 || got.Emails[0].Status != "queued" {
		t.Fatalf("get: %+v", got)
	}
	if _, err := runs.Get(f.ctx, f.other, run); !errors.Is(err, automations.ErrRunNotFound) {
		t.Fatalf("foreign get: %v", err)
	}
	if _, err := runs.Get(f.ctx, f.project, uuid.New()); !errors.Is(err, automations.ErrRunNotFound) {
		t.Fatalf("unknown get: %v", err)
	}

	// A second run of the same automation (another contact), then list.
	var automation uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT automation_id FROM automation_runs WHERE id = $1`, run).Scan(&automation); err != nil {
		t.Fatal(err)
	}
	var trigger string
	if err := f.pool.QueryRow(f.ctx, `SELECT trigger_event FROM automations WHERE id = $1`, automation).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	other := f.contact(t, f.project, "second@example.test")
	ev, err := f.events(1000).Report(f.ctx, f.project, automations.EventInput{Name: trigger, ContactID: other})
	if err != nil || len(ev.Runs) != 1 {
		t.Fatalf("second run: %+v %v", ev, err)
	}
	page, more, err := runs.List(f.ctx, f.project, automation, automations.RunFilter{}, uuid.Nil, 1)
	if err != nil || !more || len(page) != 1 || page[0].ID != ev.Runs[0].ID || page[0].Emails != nil {
		t.Fatalf("list page 1: %+v %v %v", page, more, err)
	}
	page, more, err = runs.List(f.ctx, f.project, automation, automations.RunFilter{}, page[0].ID, 1)
	if err != nil || more || len(page) != 1 || page[0].ID != run {
		t.Fatalf("list page 2: %+v %v %v", page, more, err)
	}
	if page, _, _ := runs.List(f.ctx, f.project, automation, automations.RunFilter{ContactID: contact}, uuid.Nil, 10); len(page) != 1 || page[0].ID != run {
		t.Fatalf("contact filter: %+v", page)
	}
	if _, _, err := runs.List(f.ctx, f.other, automation, automations.RunFilter{}, uuid.Nil, 10); !errors.Is(err, automations.ErrNotFound) {
		t.Fatalf("foreign automation: %v", err)
	}

	// Cancel: only active runs; foreign ids look unknown.
	if _, err := runs.Cancel(f.ctx, f.other, run); !errors.Is(err, automations.ErrRunNotFound) {
		t.Fatalf("foreign cancel: %v", err)
	}
	c, err := runs.Cancel(f.ctx, f.project, run)
	if err != nil || c.Status != automations.RunCanceled || c.Reason == nil || *c.Reason != automations.ReasonCanceled || c.NextStepAt != nil || c.FinishedAt == nil {
		t.Fatalf("cancel: %+v %v", c, err)
	}
	if _, err := runs.Cancel(f.ctx, f.project, run); !errors.Is(err, automations.ErrRunNotCancelable) {
		t.Fatalf("second cancel: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE id = $1 AND trigger_data IS NULL`, run); n != 1 {
		t.Fatal("trigger data kept after cancel")
	}
	// The waiting step job then does nothing.
	f.step(t, e, run, 2)
	if n := f.count(t, `SELECT count(*) FROM emails WHERE automation_run_id = $1`, run); n != 1 {
		t.Fatalf("%d emails after cancel", n)
	}
	if page, _, _ := runs.List(f.ctx, f.project, automation, automations.RunFilter{Status: automations.RunCanceled}, uuid.Nil, 10); len(page) != 1 {
		t.Fatalf("status filter: %+v", page)
	}
}

// A cancel waits for a step job holding the run: it cancels a run the job
// left waiting, refuses one the job ended, and gives up (busy) when the lock
// outlasts its lock timeout.
func TestRunCancelWaitsForStepJob(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	runs := automations.NewRuns(f.pool)
	for name, tc := range map[string]struct {
		change string // what the "step job" does before it commits
		hold   time.Duration
		want   error
	}{
		"job leaves the run waiting": {`UPDATE automation_runs SET step = 2, next_step_at = now() + interval '1 hour' WHERE id = $1`, 300 * time.Millisecond, nil},
		"job ends the run": {`UPDATE automation_runs SET status = 'completed', step = 3, next_step_at = NULL, trigger_data = NULL, finished_at = now() WHERE id = $1`,
			300 * time.Millisecond, automations.ErrRunNotCancelable},
		"job holds the lock too long": {`SELECT $1::uuid`, 4 * time.Second, automations.ErrBusy},
	} {
		t.Run(name, func(t *testing.T) {
			run, _ := f.startRun(t, "", f.sendStep(f.template, "1"), wait60, f.sendStep(f.template, "1"))
			tx, err := f.pool.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(f.ctx) //nolint:errcheck // committed below
			if _, err := tx.Exec(f.ctx, `SELECT id FROM automation_runs WHERE id = $1 FOR NO KEY UPDATE`, run); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := runs.Cancel(f.ctx, f.project, run)
				done <- err
			}()
			time.Sleep(tc.hold)
			if _, err := tx.Exec(f.ctx, tc.change, run); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			switch {
			case tc.want == nil && err != nil:
				t.Fatalf("cancel: %v", err)
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Fatalf("cancel: %v, want %v", err, tc.want)
			}
			if tc.want == nil {
				if s := f.run(t, run); s.status != automations.RunCanceled {
					t.Fatalf("run %+v", s)
				}
			}
		})
	}
}
