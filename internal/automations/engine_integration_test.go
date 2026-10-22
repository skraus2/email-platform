//go:build integration

package automations_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/automations"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/templates"
)

func (f *fixture) engine() *automations.Engine {
	return automations.NewEngine(f.pool, f.enq, slog.New(slog.DiscardHandler), templates.NewCache(1<<20, 1<<20))
}

// startRun creates an automation with the steps, a subscribed contact and a
// run triggered for it; it returns the run and the contact.
func (f *fixture) startRun(t *testing.T, data string, steps ...string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	in := f.input(steps...)
	in.TriggerEvent = "go." + uuid.NewString()[:8]
	if _, err := f.store.Create(f.ctx, f.project, in); err != nil {
		t.Fatal(err)
	}
	c := f.contact(t, f.project, uuid.NewString()[:8]+"@example.test")
	if _, err := f.pool.Exec(f.ctx, `UPDATE contacts SET attributes = '{"name":"Ann"}' WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	ev, err := f.events(1000).Report(f.ctx, f.project, automations.EventInput{Name: in.TriggerEvent, ContactID: c, Data: []byte(data)})
	if err != nil || len(ev.Runs) != 1 {
		t.Fatalf("start run: %+v %v", ev, err)
	}
	return ev.Runs[0].ID, c
}

func (f *fixture) step(t *testing.T, e *automations.Engine, run uuid.UUID, step int) {
	t.Helper()
	if err := e.Step(f.ctx, jobs.AutomationStepArgs{RunID: run, ProjectID: f.project, Step: step}, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
		t.Fatalf("step %d: %v", step, err)
	}
}

type runState struct {
	status, reason string
	step           int
	due, data      bool
}

func (f *fixture) run(t *testing.T, id uuid.UUID) runState {
	t.Helper()
	var s runState
	var reason *string
	if err := f.pool.QueryRow(f.ctx, `SELECT status, reason, step, next_step_at IS NOT NULL, trigger_data IS NOT NULL FROM automation_runs WHERE id = $1`, id).
		Scan(&s.status, &reason, &s.step, &s.due, &s.data); err != nil {
		t.Fatal(err)
	}
	if reason != nil {
		s.reason = *reason
	}
	return s
}

const wait60 = `{"type":"wait","seconds":60}`

func TestEngineSendWaitSend(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	run, contact := f.startRun(t, `{"order":"A-7"}`, f.sendStep(f.template, "1"), wait60, f.sendStep(f.template, "1"))

	before := time.Now()
	f.step(t, e, run, 0)
	if s := f.run(t, run); s.status != automations.RunActive || s.step != 2 || !s.due || !s.data {
		t.Fatalf("after first send + wait: %+v", s)
	}
	var subject, text, html, url string
	var step int16
	if err := f.pool.QueryRow(f.ctx, `SELECT e.subject, c.text_body, c.html_body, c.list_unsubscribe_url, e.automation_step
		FROM emails e JOIN email_contents c ON c.email_id = e.id
		WHERE e.automation_run_id = $1 AND e.contact_id = $2 AND e.status = 'queued'`, run, contact).Scan(&subject, &text, &html, &url, &step); err != nil {
		t.Fatalf("first email: %v", err)
	}
	if subject != "Hello Ann" || !strings.Contains(text, "Hi Ann A-7") || !strings.Contains(html, `href="https://app.example.test/u?t=`) ||
		!strings.HasPrefix(url, "https://app.example.test/u?t=") || strings.Contains(url, "{token}") || step != 0 {
		t.Fatalf("rendered email: %q %q %q %q %d", subject, text, html, url, step)
	}
	// The email's dispatch job is bulk; the next step is scheduled for the
	// end of the wait.
	if n := f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'send_email' AND priority = 2`); n != 1 {
		t.Fatalf("%d bulk send jobs", n)
	}
	var at time.Time
	if err := f.pool.QueryRow(f.ctx, `SELECT scheduled_at FROM river_job WHERE kind = 'automation_step' AND args->>'step' = '2'`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at.Before(before.Add(59*time.Second)) || at.After(time.Now().Add(61*time.Second)) {
		t.Fatalf("next step at %v", at)
	}

	// The same job again, and a stale one, change nothing.
	f.step(t, e, run, 0)
	f.step(t, e, run, 1)
	if n := f.count(t, `SELECT count(*) FROM emails WHERE automation_run_id = $1`, run); n != 1 {
		t.Fatalf("%d emails after repeated jobs", n)
	}

	f.step(t, e, run, 2)
	if s := f.run(t, run); s.status != automations.RunCompleted || s.reason != "" || s.step != 3 || s.due || s.data {
		t.Fatalf("completed: %+v", s)
	}
	if n := f.count(t, `SELECT count(*) FROM emails WHERE automation_run_id = $1`, run); n != 2 {
		t.Fatalf("%d emails", n)
	}
	f.step(t, e, run, 2)
	if n := f.count(t, `SELECT count(*) FROM emails WHERE automation_run_id = $1`, run); n != 2 {
		t.Fatalf("job after completion sent again: %d", n)
	}
}

func TestEngineConditions(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	send := f.sendStep(f.template, "1")
	cases := []struct {
		name      string
		condition string
		event     string // reported for the contact after the run started
		want      runState
	}{
		{"attribute holds", `{"type":"condition","attribute":"name","op":"eq","value":"Ann"}`, "", runState{status: "completed", step: 2}},
		{"attribute fails", `{"type":"condition","attribute":"name","op":"neq","value":"Ann"}`, "", runState{status: "exited", reason: "condition_not_met"}},
		{"missing attribute", `{"type":"condition","attribute":"plan","op":"exists"}`, "", runState{status: "exited", reason: "condition_not_met"}},
		{"not occurred holds", `{"type":"condition","event":"order.completed","op":"not_occurred"}`, "", runState{status: "completed", step: 2}},
		{"not occurred fails", `{"type":"condition","event":"order.completed","op":"not_occurred"}`, "order.completed", runState{status: "exited", reason: "condition_not_met"}},
		{"occurred holds", `{"type":"condition","event":"order.completed","op":"occurred"}`, "order.completed", runState{status: "completed", step: 2}},
		{"other event", `{"type":"condition","event":"order.completed","op":"occurred"}`, "order.refunded", runState{status: "exited", reason: "condition_not_met"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, contact := f.startRun(t, "", tc.condition, send)
			if tc.event != "" {
				if _, err := f.events(1000).Report(f.ctx, f.project, automations.EventInput{Name: tc.event, ContactID: contact}); err != nil {
					t.Fatal(err)
				}
			}
			f.step(t, e, run, 0)
			got := f.run(t, run)
			if got.status != tc.want.status || got.reason != tc.want.reason || (tc.want.step != 0 && got.step != tc.want.step) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			emails := f.count(t, `SELECT count(*) FROM emails WHERE automation_run_id = $1`, run)
			if (tc.want.status == "completed") != (emails == 1) {
				t.Fatalf("%d emails", emails)
			}
		})
	}
	// The trigger event itself never counts as "occurred after the start".
	run, _ := f.startRunNamed(t, "order.completed", `{"type":"condition","event":"order.completed","op":"occurred"}`, send)
	f.step(t, e, run, 0)
	if s := f.run(t, run); s.status != "exited" {
		t.Fatalf("trigger counted as a later event: %+v", s)
	}
}

// startRunNamed is startRun with a fixed trigger name.
func (f *fixture) startRunNamed(t *testing.T, trigger string, steps ...string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	in := f.input(steps...)
	in.TriggerEvent = trigger
	if _, err := f.store.Create(f.ctx, f.project, in); err != nil {
		t.Fatal(err)
	}
	c := f.contact(t, f.project, uuid.NewString()[:8]+"@example.test")
	ev, err := f.events(1000).Report(f.ctx, f.project, automations.EventInput{Name: trigger, ContactID: c})
	if err != nil || len(ev.Runs) != 1 {
		t.Fatalf("start run: %+v %v", ev, err)
	}
	return ev.Runs[0].ID, c
}

func TestEngineEndsRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	send := f.sendStep(f.template, "1")
	cases := []struct {
		name  string
		data  string
		setup func(run, contact uuid.UUID)
		want  runState
	}{
		{"unsubscribed", "", func(_, c uuid.UUID) {
			f.exec(t, `UPDATE contacts SET status = 'unsubscribed' WHERE id = $1`, c)
		}, runState{status: "exited", reason: "unsubscribed"}},
		{"suppressed", "", func(_, c uuid.UUID) {
			f.exec(t, `INSERT INTO suppressions (project_id, recipient, reason) SELECT project_id, upper(email), 'manual' FROM contacts WHERE id = $1`, c)
		}, runState{status: "exited", reason: "suppressed"}},
		{"render failed", `{"order":5}`, nil, runState{status: "failed", reason: "render_failed"}},
		{"archived", "", func(r, _ uuid.UUID) {
			f.exec(t, `UPDATE automations SET status = 'archived', archived_at = now() WHERE id = (SELECT automation_id FROM automation_runs WHERE id = $1)`, r)
		}, runState{status: "canceled", reason: "automation_archived"}},
		{"project disabled", "", func(_, _ uuid.UUID) {
			f.exec(t, `UPDATE projects SET status = 'disabled' WHERE id = $1`, f.project)
		}, runState{status: "exited", reason: "project_disabled"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, contact := f.startRun(t, tc.data, send)
			if tc.setup != nil {
				tc.setup(run, contact)
			}
			f.step(t, e, run, 0)
			got := f.run(t, run)
			if got.status != tc.want.status || got.reason != tc.want.reason || got.due || got.data {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if n := f.count(t, `SELECT count(*) FROM emails WHERE automation_run_id = $1`, run); n != 0 {
				t.Fatalf("%d emails", n)
			}
			f.exec(t, `UPDATE projects SET status = 'active' WHERE id = $1`, f.project)
		})
	}
	// A run whose contact was deleted is gone: the job does nothing.
	run, contact := f.startRun(t, "", send)
	f.exec(t, `DELETE FROM contacts WHERE id = $1`, contact)
	f.step(t, e, run, 0)
	if n := f.count(t, `SELECT count(*) FROM emails WHERE automation_id IS NOT NULL AND contact_id IS NULL`); n != 0 {
		t.Fatalf("%d emails for a deleted contact", n)
	}
}

func (f *fixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestEngineFinalAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	run, _ := f.startRun(t, "", f.sendStep(f.template, "1"))
	// A send step whose template version is gone fails internally.
	f.exec(t, `UPDATE automations SET steps = jsonb_set(steps, '{0,template_version}', '99') WHERE id = (SELECT automation_id FROM automation_runs WHERE id = $1)`, run)
	args := jobs.AutomationStepArgs{RunID: run, ProjectID: f.project, Step: 0}
	if err := e.Step(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err == nil {
		t.Fatal("internal error not returned for a retry")
	}
	if s := f.run(t, run); s.status != automations.RunActive {
		t.Fatalf("run ended before the final attempt: %+v", s)
	}
	if err := e.Step(f.ctx, args, jobs.Attempt{Number: 25, MaxAttempts: 25}); err != nil {
		t.Fatalf("final attempt: %v", err)
	}
	if s := f.run(t, run); s.status != automations.RunFailed || s.reason != automations.ReasonInternalError || s.data {
		t.Fatalf("after final attempt: %+v", s)
	}
	// A stale job's final failure never touches a run at another step.
	other, _ := f.startRun(t, "", f.sendStep(f.template, "1"), wait60, f.sendStep(f.template, "1"))
	f.step(t, e, other, 0)
	if err := automations.FailRun(e, f.ctx, jobs.AutomationStepArgs{RunID: other, ProjectID: f.project, Step: 0}, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if s := f.run(t, other); s.status != automations.RunActive || s.step != 2 {
		t.Fatalf("stale failure touched the run: %+v", s)
	}
}

func TestEngineRequeueOverdue(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	run, _ := f.startRun(t, "", f.sendStep(f.template, "1"))
	// Lose the step job, as after a discard.
	f.exec(t, `DELETE FROM river_job WHERE kind = 'automation_step'`)
	f.exec(t, `UPDATE automation_runs SET next_step_at = now() - interval '16 minutes' WHERE id = $1`, run)
	if err := e.Requeue(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'automation_step' AND args->>'run_id' = $1`, run.String()); n != 1 {
		t.Fatalf("%d jobs after requeue", n)
	}
	// While the job exists, a second pass inserts nothing.
	if err := e.Requeue(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'automation_step' AND args->>'run_id' = $1`, run.String()); n != 1 {
		t.Fatalf("%d jobs after a second requeue", n)
	}
	// A run due less than the grace ago is left alone.
	recent, _ := f.startRun(t, "", f.sendStep(f.template, "1"))
	f.exec(t, `DELETE FROM river_job WHERE kind = 'automation_step' AND args->>'run_id' = $1`, recent.String())
	if err := e.Requeue(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM river_job WHERE kind = 'automation_step' AND args->>'run_id' = $1`, recent.String()); n != 0 {
		t.Fatalf("recent run requeued")
	}
}

// A step job and the deletion of its contact take their locks in the same
// order (contact, then run): racing them never deadlocks.
func TestEngineRacesContactDeletion(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	cs := contacts.NewStore(f.pool, 1000)
	for range 20 {
		run, contact := f.startRun(t, "", f.sendStep(f.template, "1"), wait60, f.sendStep(f.template, "1"))
		var wg sync.WaitGroup
		var stepErr, delErr error
		wg.Go(func() {
			stepErr = e.Step(f.ctx, jobs.AutomationStepArgs{RunID: run, ProjectID: f.project, Step: 0}, jobs.Attempt{Number: 1, MaxAttempts: 25})
		})
		wg.Go(func() { delErr = cs.Delete(f.ctx, f.project, contact) })
		wg.Wait()
		if stepErr != nil || delErr != nil {
			t.Fatalf("step: %v, delete: %v", stepErr, delErr)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM automation_runs`); n != 0 {
		t.Fatalf("%d runs left after their contacts were deleted", n)
	}
}

// A step job whose project id does not match its run does nothing, and so
// does a final-attempt failure mark.
func TestEngineIgnoresForeignProject(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	run, _ := f.startRun(t, `{"order":"A-1"}`, f.sendStep(f.template, "1"))
	jobsBefore := f.count(t, `SELECT count(*) FROM river_job`)
	for _, args := range []jobs.AutomationStepArgs{
		{RunID: run, ProjectID: f.other, Step: 0},
		{RunID: uuid.New(), ProjectID: f.project, Step: 0},
	} {
		if err := e.Step(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
			t.Fatalf("%+v: %v", args, err)
		}
		if err := automations.FailRun(e, f.ctx, args, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
	}
	if s := f.run(t, run); s.status != automations.RunActive || s.step != 0 || !s.due || !s.data {
		t.Fatalf("run changed by a foreign job: %+v", s)
	}
	if n := f.count(t, `SELECT count(*) FROM emails`); n != 0 {
		t.Fatalf("%d emails from a foreign job", n)
	}
	if n := f.count(t, `SELECT count(*) FROM river_job`); n != jobsBefore {
		t.Fatalf("foreign job enqueued %d jobs", n-jobsBefore)
	}
}

// Even if a version whose subject prints the unsubscribe URL got into a
// definition, the token never reaches a stored subject.
func TestEngineKeepsTokenOutOfSubject(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	tmpl, err := f.templates.Create(f.ctx, f.project, "leaky", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.templates.Publish(f.ctx, f.project, tmpl.ID, templates.Source{Subject: "Hi {{.unsubscribe_url}}", Text: "x {{.unsubscribe_url}}"},
		[]byte(`{"unsubscribe_url":{"type":"string","required":true}}`)); err != nil {
		t.Fatal(err)
	}
	run, _ := f.startRun(t, "", f.sendStep(f.template, "1"))
	f.exec(t, `UPDATE automations SET steps = jsonb_set(steps, '{0,template_id}', to_jsonb($2::text))
		WHERE id = (SELECT automation_id FROM automation_runs WHERE id = $1)`, run, tmpl.ID.String())
	f.step(t, e, run, 0)
	if s := f.run(t, run); s.status != automations.RunFailed || s.reason != automations.ReasonRenderFailed {
		t.Fatalf("run: %+v", s)
	}
	if n := f.count(t, `SELECT count(*) FROM emails`); n != 0 {
		t.Fatalf("%d emails stored", n)
	}
}

// Ended runs are final: no step job, final-attempt mark or overdue pass
// changes them.
func TestEngineTerminalRunsAreFinal(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	for _, end := range []struct{ status, reason string }{
		{"completed", ""}, {"exited", "condition_not_met"}, {"canceled", "canceled"}, {"failed", "internal_error"},
	} {
		t.Run(end.status, func(t *testing.T) {
			run, _ := f.startRun(t, "", f.sendStep(f.template, "1"))
			var reason *string
			if end.reason != "" {
				reason = &end.reason
			}
			f.exec(t, `UPDATE automation_runs SET status = $2, reason = $3, next_step_at = NULL, trigger_data = NULL,
				finished_at = now() - interval '1 hour' WHERE id = $1`, run, end.status, reason)
			var before string
			if err := f.pool.QueryRow(f.ctx, `SELECT row_to_json(r)::text FROM automation_runs r WHERE id = $1`, run).Scan(&before); err != nil {
				t.Fatal(err)
			}
			jobsBefore := f.count(t, `SELECT count(*) FROM river_job`)
			args := jobs.AutomationStepArgs{RunID: run, ProjectID: f.project, Step: 0}
			if err := e.Step(f.ctx, args, jobs.Attempt{Number: 1, MaxAttempts: 25}); err != nil {
				t.Fatal(err)
			}
			if err := automations.FailRun(e, f.ctx, args, errors.New("boom")); err != nil {
				t.Fatal(err)
			}
			if err := e.Requeue(f.ctx); err != nil {
				t.Fatal(err)
			}
			var after string
			if err := f.pool.QueryRow(f.ctx, `SELECT row_to_json(r)::text FROM automation_runs r WHERE id = $1`, run).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("ended run changed:\n%s\n%s", before, after)
			}
			if n := f.count(t, `SELECT count(*) FROM emails WHERE automation_run_id = $1`, run); n != 0 {
				t.Fatalf("%d emails", n)
			}
			if n := f.count(t, `SELECT count(*) FROM river_job`); n != jobsBefore {
				t.Fatalf("%d new jobs", n-jobsBefore)
			}
		})
	}
}

// A hard shutdown (cancelled context) on the final attempt marks nothing;
// a timed-out final attempt fails the run.
func TestEngineFinalAttemptContext(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	run, _ := f.startRun(t, "", f.sendStep(f.template, "1"))
	canceled, cancel := context.WithCancel(f.ctx)
	cancel()
	final := jobs.Attempt{Number: 25, MaxAttempts: 25}
	if err := e.Step(canceled, jobs.AutomationStepArgs{RunID: run, ProjectID: f.project, Step: 0}, final); err == nil {
		t.Fatal("cancelled final attempt returned no error")
	}
	if s := f.run(t, run); s.status != automations.RunActive || s.step != 0 {
		t.Fatalf("hard shutdown marked the run: %+v", s)
	}
	expired, cancel2 := context.WithDeadline(f.ctx, time.Now().Add(-time.Second))
	defer cancel2()
	if err := e.Step(expired, jobs.AutomationStepArgs{RunID: run, ProjectID: f.project, Step: 0}, final); err != nil {
		t.Fatalf("timed-out final attempt: %v", err)
	}
	if s := f.run(t, run); s.status != automations.RunFailed || s.reason != automations.ReasonInternalError {
		t.Fatalf("timed-out final attempt: %+v", s)
	}
}

// Variables: shared values < contact attributes < trigger event data (the
// unsubscribe URL, set last by the platform, is covered by TestMergeLayers).
func TestEngineVariablePrecedence(t *testing.T) {
	t.Parallel()
	f := newFixture(t, 50)
	e := f.engine()
	send := strings.Replace(f.sendStep(f.template, "1"), `{"plan":"pro"}`, `{"name":"Shared","order":"shared"}`, 1)
	run, contact := f.startRun(t, `{"order":"event"}`, send)
	f.exec(t, `UPDATE contacts SET attributes = '{"name":"Attr","order":"attr"}' WHERE id = $1`, contact)
	f.step(t, e, run, 0)
	var text string
	if err := f.pool.QueryRow(f.ctx, `SELECT c.text_body FROM emails e JOIN email_contents c ON c.email_id = e.id WHERE e.automation_run_id = $1`, run).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "Hi Attr event\n") {
		t.Fatalf("text %q, want the attribute's name and the event's order", text)
	}
}
