//go:build integration

package automations_test

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/retention"
)

func (f *fixture) events(perMinute int) *automations.Events {
	return automations.NewEvents(f.pool, f.enq, slog.New(slog.DiscardHandler), perMinute)
}

func (f *fixture) contact(t *testing.T, project uuid.UUID, email string) uuid.UUID {
	t.Helper()
	src := "test"
	c, err := contacts.NewStore(f.pool, 1000).Create(f.ctx, project, contacts.CreateInput{Email: email, Status: contacts.StatusSubscribed, ConsentSource: &src})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func (f *fixture) automation(t *testing.T, trigger string, reentry bool, status string) uuid.UUID {
	t.Helper()
	in := f.input()
	in.TriggerEvent, in.AllowReentry = trigger, reentry
	if status != "" {
		in.Status = &status
	}
	a, err := f.store.Create(f.ctx, f.project, in)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func TestReportStartsRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	once := f.automation(t, "user.signed_up", false, "")
	again := f.automation(t, "user.signed_up", true, "")
	f.automation(t, "user.signed_up", false, automations.StatusPaused)
	f.automation(t, "other", false, "")
	archived := f.automation(t, "user.signed_up", false, "")
	st := automations.StatusArchived
	if _, err := f.store.Update(f.ctx, f.project, archived, automations.UpdateInput{Status: &st}); err != nil {
		t.Fatal(err)
	}
	c := f.contact(t, f.project, "Ann@Example.test")
	ev := f.events(1000)

	got, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: "user.signed_up", Email: "ann@example.TEST", Data: []byte(`{"order":"A-1"}`)})
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if got.ContactID != c || len(got.Runs) != 2 || got.Replayed {
		t.Fatalf("first report: %+v", got)
	}
	started := map[uuid.UUID]bool{}
	for _, r := range got.Runs {
		started[r.AutomationID] = true
	}
	if !started[once] || !started[again] {
		t.Fatalf("runs of the wrong automations: %+v", got.Runs)
	}
	// Runs carry the data and their first step job.
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE trigger_event_id = $1 AND status = 'active' AND step = 0
		AND trigger_data = '{"order":"A-1"}'::jsonb AND next_step_at IS NOT NULL`, got.ID); n != 2 {
		t.Fatalf("%d runs with data", n)
	}
	for _, r := range got.Runs {
		if n := f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'automation_step' AND queue = 'automations'
			AND args->>'step' = '0' AND args->>'run_id' = $1 AND args->>'project_id' = $2`, r.ID.String(), f.project.String()); n != 1 {
			t.Fatalf("%d step jobs for run %s", n, r.ID)
		}
	}
	// The event row stores no data.
	if n := f.count(t, `SELECT count(*) FROM contact_events WHERE id = $1 AND name = 'user.signed_up' AND contact_id = $2`, got.ID, c); n != 1 {
		t.Fatal("event not stored")
	}

	// While both runs are active, nothing new starts.
	got, err = ev.Report(f.ctx, f.project, automations.EventInput{Name: "user.signed_up", ContactID: c})
	if err != nil || len(got.Runs) != 0 {
		t.Fatalf("second report: %+v %v", got, err)
	}
	// After the runs end, only the automation allowing re-entry starts again.
	if _, err := f.pool.Exec(f.ctx, `UPDATE automation_runs SET status = 'completed', next_step_at = NULL, trigger_data = NULL, finished_at = now(), step = 3`); err != nil {
		t.Fatal(err)
	}
	got, err = ev.Report(f.ctx, f.project, automations.EventInput{Name: "user.signed_up", ContactID: c})
	if err != nil || len(got.Runs) != 1 || got.Runs[0].AutomationID != again {
		t.Fatalf("re-entry: %+v %v", got, err)
	}
	// An event without automations is still stored.
	if got, err = ev.Report(f.ctx, f.project, automations.EventInput{Name: "order.completed", ContactID: c}); err != nil || len(got.Runs) != 0 {
		t.Fatalf("plain event: %+v %v", got, err)
	}
}

func TestReportRefuses(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	foreign := f.contact(t, f.other, "x@example.test")
	f.contact(t, f.project, "a@example.test")
	ev := f.events(1000)
	for name, in := range map[string]automations.EventInput{
		"foreign id":    {Name: "e", ContactID: foreign},
		"unknown id":    {Name: "e", ContactID: uuid.New()},
		"foreign email": {Name: "e", Email: "x@example.test"},
		"unknown email": {Name: "e", Email: "nobody@example.test"},
	} {
		if _, err := ev.Report(f.ctx, f.project, in); !errors.Is(err, automations.ErrContactNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, tc := range map[string]struct {
		in    automations.EventInput
		field string
	}{
		"bad name":    {automations.EventInput{Name: "Signup", Email: "a@example.test"}, "name"},
		"no contact":  {automations.EventInput{Name: "e"}, "contact_id"},
		"both":        {automations.EventInput{Name: "e", Email: "a@example.test", ContactID: uuid.New()}, "contact_id"},
		"bad email":   {automations.EventInput{Name: "e", Email: "a b"}, "email"},
		"nested data": {automations.EventInput{Name: "e", Email: "a@example.test", Data: []byte(`{"a":{}}`)}, "data.a"},
		"reserved":    {automations.EventInput{Name: "e", Email: "a@example.test", Data: []byte(`{"unsubscribe_url":"x"}`)}, "data.unsubscribe_url"},
		"not object":  {automations.EventInput{Name: "e", Email: "a@example.test", Data: []byte(`[1]`)}, "data"},
	} {
		_, err := ev.Report(f.ctx, f.project, tc.in)
		var e *automations.Error
		if !errors.As(err, &e) || e.Field != tc.field {
			t.Errorf("%s: %v (want field %s)", name, err, tc.field)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM contact_events`); n != 0 {
		t.Fatalf("%d events stored by refused reports", n)
	}
}

func TestReportIdempotency(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	f.automation(t, "user.signed_up", true, "")
	c := f.contact(t, f.project, "a@example.test")
	ev := f.events(1000)
	in := automations.EventInput{Name: "user.signed_up", ContactID: c, Data: []byte(`{"b":1,"a":"x"}`), IdempotencyKey: "signup-1"}

	var wg sync.WaitGroup
	results := make(chan automations.Event, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			got, err := ev.Report(f.ctx, f.project, in)
			if err != nil {
				errs <- err
				return
			}
			results <- got
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent report: %v", err)
	}
	var first automations.Event
	replays := 0
	for got := range results {
		if first.ID == uuid.Nil {
			first = got
		}
		if got.ID != first.ID || len(got.Runs) != 1 || got.Runs[0].ID != first.Runs[0].ID {
			t.Fatalf("different responses: %+v vs %+v", got, first)
		}
		if got.Replayed {
			replays++
		}
	}
	if replays != 7 {
		t.Fatalf("%d replays, want 7", replays)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs`); n != 1 {
		t.Fatalf("%d runs", n)
	}
	// Same payload in another key order replays; a different payload is 409.
	same := in
	same.Data = []byte(`{"a":"x", "b":1}`)
	if got, err := ev.Report(f.ctx, f.project, same); err != nil || !got.Replayed || got.ID != first.ID {
		t.Fatalf("canonical replay: %+v %v", got, err)
	}
	other := in
	other.Data = []byte(`{"a":"y"}`)
	if _, err := ev.Report(f.ctx, f.project, other); !errors.Is(err, automations.ErrKeyReused) {
		t.Fatalf("different payload: %v", err)
	}
	byEmail := in
	byEmail.ContactID, byEmail.Email = uuid.Nil, "a@example.test"
	if _, err := ev.Report(f.ctx, f.project, byEmail); !errors.Is(err, automations.ErrKeyReused) {
		t.Fatalf("other contact reference: %v", err)
	}
	// Keys are per project.
	oc := f.contact(t, f.other, "a@example.test")
	if got, err := ev.Report(f.ctx, f.other, automations.EventInput{Name: "user.signed_up", ContactID: oc, IdempotencyKey: "signup-1"}); err != nil || got.Replayed {
		t.Fatalf("other project, same key: %+v %v", got, err)
	}
	if n := f.count(t, `SELECT count(*) FROM contact_events`); n != 2 {
		t.Fatalf("%d events", n)
	}
}

func TestReportConcurrentSameContact(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	f.automation(t, "user.signed_up", true, "")
	c := f.contact(t, f.project, "a@example.test")
	ev := f.events(1000)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: "user.signed_up", ContactID: c}); err != nil {
				t.Errorf("report: %v", err)
			}
		})
	}
	wg.Wait()
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE status = 'active'`); n != 1 {
		t.Fatalf("%d active runs for one contact", n)
	}
	if n := f.count(t, `SELECT count(*) FROM contact_events`); n != 10 {
		t.Fatalf("%d events", n)
	}
}

// awayFromMinuteEnd waits out the last seconds of a minute, so a test's
// requests share one rate limit window.
func awayFromMinuteEnd() {
	if s := time.Now().Second(); s >= 50 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
}

func TestReportRateLimit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	awayFromMinuteEnd()
	c := f.contact(t, f.project, "a@example.test")
	ev := f.events(3)
	in := automations.EventInput{Name: "e", ContactID: c, IdempotencyKey: "k1"}
	for i := range 3 {
		in.IdempotencyKey = "k" + string(rune('1'+i))
		if _, err := ev.Report(f.ctx, f.project, in); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	// A replay is not counted and not refused.
	in.IdempotencyKey = "k1"
	if got, err := ev.Report(f.ctx, f.project, in); err != nil || !got.Replayed {
		t.Fatalf("replay at the limit: %v", err)
	}
	in.IdempotencyKey = "k9"
	_, err := ev.Report(f.ctx, f.project, in)
	var rl *automations.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter < 1e9 || rl.RetryAfter > 60e9 {
		t.Fatalf("over the limit: %v", err)
	}
	// Refused requests are not counted; other projects have their own window.
	oc := f.contact(t, f.other, "a@example.test")
	if _, err := ev.Report(f.ctx, f.other, automations.EventInput{Name: "e", ContactID: oc}); err != nil {
		t.Fatalf("other project: %v", err)
	}
	if n := f.count(t, `SELECT count FROM contact_event_windows WHERE project_id = $1`, f.project); n != 3 {
		t.Fatalf("window count %d", n)
	}
}

// Concurrent reports all pass the read-only pre-check; the count in the
// transaction refuses the excess and rolls back their events, runs and jobs.
func TestReportRateLimitInTransaction(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	awayFromMinuteEnd()
	f.automation(t, "e", true, "")
	ev := f.events(3)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := range 10 {
		c := f.contact(t, f.project, fmt.Sprintf("c%d@example.test", i))
		wg.Go(func() {
			_, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: "e", ContactID: c})
			var rl *automations.RateLimitError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.As(err, &rl):
				limited++
			default:
				t.Errorf("report: %v", err)
			}
		})
	}
	wg.Wait()
	if ok != 3 || limited != 7 {
		t.Fatalf("ok=%d limited=%d, want 3 and 7", ok, limited)
	}
	for q, want := range map[string]int{
		`SELECT count FROM contact_event_windows WHERE project_id = $1`:                                    3,
		`SELECT count(*) FROM contact_events WHERE project_id = $1`:                                        3,
		`SELECT count(*) FROM automation_runs WHERE project_id = $1`:                                       3,
		`SELECT count(*) FROM river_job WHERE kind = 'automation_step' AND args->>'project_id' = $1::text`: 3,
	} {
		if n := f.count(t, q, f.project); n != want {
			t.Errorf("%s: %d, want %d", q, n, want)
		}
	}
}

// A refused lookup counts, so the 422 cannot probe addresses without limit.
func TestReportUnknownContactCounts(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	awayFromMinuteEnd()
	ev := f.events(2)
	for range 2 {
		if _, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: "e", Email: "nobody@example.test"}); !errors.Is(err, automations.ErrContactNotFound) {
			t.Fatalf("unknown contact: %v", err)
		}
	}
	_, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: "e", Email: "nobody@example.test"})
	var rl *automations.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("third probe: %v", err)
	}
}

func TestContactDeletionErasesEventsAndRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	f.automation(t, "user.signed_up", false, "")
	c := f.contact(t, f.project, "a@example.test")
	if _, err := f.events(1000).Report(f.ctx, f.project, automations.EventInput{Name: "user.signed_up", ContactID: c}); err != nil {
		t.Fatal(err)
	}
	if err := contacts.NewStore(f.pool, 1000).Delete(f.ctx, f.project, c); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT (SELECT count(*) FROM contact_events) + (SELECT count(*) FROM automation_runs)`); n != 0 {
		t.Fatalf("%d rows left after erasure", n)
	}
}

func TestEventRetention(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	once := f.automation(t, "a", false, "")
	again := f.automation(t, "b", true, "")
	c := f.contact(t, f.project, "a@example.test")
	ev := f.events(1000)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: name, ContactID: c}); err != nil {
			t.Fatal(err)
		}
	}
	// Re-entry runs that must survive: one still active, one finished
	// recently (both of other contacts, added after the backdating below).
	if _, err := f.pool.Exec(f.ctx, `UPDATE automation_runs SET status = 'completed', next_step_at = NULL, trigger_data = NULL,
		finished_at = now() - interval '100 days', step = 3`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE contact_events SET created_at = now() - interval '100 days' WHERE name <> 'c'`); err != nil {
		t.Fatal(err)
	}
	active, recent := f.contact(t, f.project, "active@example.test"), f.contact(t, f.project, "recent@example.test")
	for _, c := range []uuid.UUID{active, recent} {
		if _, err := ev.Report(f.ctx, f.project, automations.EventInput{Name: "b", ContactID: c}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE automation_runs SET status = 'completed', next_step_at = NULL, trigger_data = NULL,
		finished_at = now() - interval '1 day', step = 3 WHERE contact_id = $1`, recent); err != nil {
		t.Fatal(err)
	}
	p := retention.Pruner{Pool: f.pool, Logger: slog.New(slog.DiscardHandler), EventRetention: retention.MinEventRetention,
		DeliveryRetention: retention.MinDeliveryRetention, ContactEventRetention: 90 * 24 * time.Hour}
	if err := p.Run(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM contact_events`); n != 3 {
		t.Fatalf("%d events left, want the 3 recent ones", n)
	}
	// Old finished runs of the re-entry automation are deleted; its active
	// and recently finished runs stay; the one-run-ever rule keeps the other.
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND contact_id = $2`, again, c); n != 0 {
		t.Fatalf("%d old re-entry runs left", n)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND contact_id IN ($2, $3)`, again, active, recent); n != 2 {
		t.Fatalf("%d of the active and recent re-entry runs left, want 2", n)
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs WHERE automation_id = $1 AND trigger_event_id IS NULL`, once); n != 1 {
		t.Fatalf("one-run automation lost its run (or its event link stayed)")
	}
}

func TestDeleteOldEventWindows(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO contact_event_windows (project_id, window_start, count)
		VALUES ($1, date_trunc('minute', now()) - interval '2 hours', 5), ($1, date_trunc('minute', now()), 1)`, f.project); err != nil {
		t.Fatal(err)
	}
	if err := automations.DeleteOldEventWindows(f.ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM contact_event_windows`); n != 1 {
		t.Fatalf("%d windows left, want the current one", n)
	}
}
