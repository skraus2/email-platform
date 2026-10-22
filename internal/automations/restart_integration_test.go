//go:build integration

package automations_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/templates"
)

type countingSink struct {
	mu   sync.Mutex
	sent map[string]int // by email id
}

func (s *countingSink) Send(_ context.Context, m delivery.Message) (delivery.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent[m.ID.String()]++
	return delivery.Result{Provider: "fake", ProviderID: "<" + m.ID.String() + ">", Status: delivery.StatusCaptured}, nil
}

// worker starts a real job worker with the automation and dispatch
// handlers; stop stops it gracefully.
func (f *fixture) worker(t *testing.T, sink *countingSink) (stop func()) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	d := emails.NewDispatcher(f.pool, sink, logger, 5*time.Second, time.Hour)
	engine := automations.NewEngine(f.pool, f.enq, logger, templates.NewCache(1<<20, 1<<20))
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: f.pool, Logger: logger,
		Handlers: jobs.Handlers{
			SendEmail: d.SendEmail, AutomationStep: engine.Step, AutomationArchive: automations.CancelArchivedRuns(f.pool),
			Cleanup: func(context.Context) error { return nil },
		},
		Retry: jobs.RetryPolicy{Base: 100 * time.Millisecond, Max: time.Second}, Concurrency: 8, AutomationConcurrency: 4,
		JobTimeout: jobs.AttemptBudget(5 * time.Second), CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if err := w.Stop(10 * time.Second); err != nil {
				t.Errorf("stop worker: %v", err)
			}
		})
	}
}

func (f *fixture) waitFor(t *testing.T, what, q string, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		n := f.count(t, q)
		if n == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d, want %d after %v", what, n, want, within)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Runs waiting on a scheduled step survive a worker stop: a new worker
// started after the due time executes every step exactly once.
func TestActiveWaitsSurviveWorkerRestart(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	const runs = 40
	in := f.input(f.sendStep(f.template, "1"), wait60, f.sendStep(f.template, "1"))
	in.TriggerEvent = "restart.test"
	a, err := f.store.Create(f.ctx, f.project, in)
	if err != nil {
		t.Fatal(err)
	}
	// Waits are at least a minute through the API; shorten this one.
	f.exec(t, `UPDATE automations SET steps = jsonb_set(steps, '{1,seconds}', '10') WHERE id = $1`, a.ID)
	ev := f.events(10_000)
	for i := range runs {
		c := f.contact(t, f.project, fmt.Sprintf("r%d@example.test", i))
		f.exec(t, `UPDATE contacts SET attributes = '{"name":"R"}' WHERE id = $1`, c)
		if _, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: in.TriggerEvent, ContactID: c}); err != nil {
			t.Fatal(err)
		}
	}
	sink := &countingSink{sent: map[string]int{}}

	stop := f.worker(t, sink)
	t.Cleanup(stop)
	f.waitFor(t, "runs waiting", `SELECT count(*) FROM automation_runs WHERE status = 'active' AND step = 2`, runs, 20*time.Second)
	f.waitFor(t, "first emails sent", `SELECT count(*) FROM emails WHERE status = 'captured'`, runs, 20*time.Second)
	stop()
	// While no worker runs, the waits come due; nothing is lost or done.
	time.Sleep(11 * time.Second)
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE status = 'active' AND step = 2 AND next_step_at < now()`); n != runs {
		t.Fatalf("%d overdue waiting runs while stopped, want %d", n, runs)
	}

	stop = f.worker(t, sink)
	t.Cleanup(stop)
	f.waitFor(t, "runs completed", `SELECT count(*) FROM automation_runs WHERE status = 'completed'`, runs, 30*time.Second)
	f.waitFor(t, "second emails sent", `SELECT count(*) FROM emails WHERE status = 'captured'`, 2*runs, 30*time.Second)
	if n := f.count(t, `SELECT count(*) FROM (SELECT automation_run_id FROM emails GROUP BY automation_run_id HAVING count(*) <> 2) x`); n != 0 {
		t.Fatalf("%d runs without exactly two emails", n)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.sent) != 2*runs {
		t.Fatalf("provider saw %d emails, want %d", len(sink.sent), 2*runs)
	}
	for id, n := range sink.sent {
		if n != 1 {
			t.Fatalf("email %s sent %d times", id, n)
		}
	}
}
