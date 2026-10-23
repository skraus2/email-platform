//go:build integration

package emails_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/metrics"
)

// sample returns the value of the series name{labels} (counter value or
// histogram sample count), 0 if absent.
func sample(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if want, ok := labels[l.GetName()]; ok && want != l.GetValue() {
					continue next
				}
			}
			if f.GetType() == dto.MetricType_HISTOGRAM {
				return float64(m.GetHistogram().GetSampleCount())
			}
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// broadcastEmailIn stores a queued email of a broadcast in the given status
// (sending or canceled) for a subscribed contact.
func broadcastEmailIn(t *testing.T, pool *pgxpool.Pool, projectID uuid.UUID, status string) jobs.SendEmailArgs {
	t.Helper()
	ctx := context.Background()
	var templateID, audienceID, contactID, broadcastID, emailID uuid.UUID
	for _, step := range []struct {
		dst  *uuid.UUID
		sql  string
		args []any
	}{
		{&templateID, "INSERT INTO templates (project_id, slug, latest_version) VALUES ($1, $2, 1) RETURNING id", []any{projectID, "news-" + uuid.NewString()[:8]}},
		{&audienceID, "INSERT INTO audiences (project_id, name) VALUES ($1, $2) RETURNING id", []any{projectID, "list-" + uuid.NewString()[:8]}},
		{&contactID, "INSERT INTO contacts (project_id, email, status, consent_source, unsubscribe_token) VALUES ($1, $2, 'subscribed', 'x', $3) RETURNING id",
			[]any{projectID, "c-" + uuid.NewString()[:8] + "@example.test", "tok_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:32] + strings.Repeat("b", 7)}},
	} {
		if err := pool.QueryRow(ctx, step.sql, step.args...).Scan(step.dst); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO template_versions (template_id, project_id, version, subject, text_body, variables)
		VALUES ($1, $2, 1, 'News', 'Bye', '{}')`, templateID, projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO broadcasts (project_id, audience_id, sender, template_id, template_version, unsubscribe_url, status, scheduled_at, started_at, finished_at)
		VALUES ($1, $2, 'noreply@example.test', $3, 1, 'https://app.example.test/u?t={token}', $4, now(), now(), CASE WHEN $4 = 'canceled' THEN now() END) RETURNING id`,
		projectID, audienceID, templateID, status).Scan(&broadcastID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO emails (project_id, sender, recipient, subject, template_id, template_version, broadcast_id, contact_id)
		VALUES ($1, 'noreply@example.test', 'c@example.test', 'News', $2, 1, $3, $4) RETURNING id`,
		projectID, templateID, broadcastID, contactID).Scan(&emailID); err != nil {
		t.Fatal(err)
	}
	return jobs.SendEmailArgs{EmailID: emailID, ProjectID: projectID}
}

func TestDispatchMetricsCountEachOutcomeOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	reg := prometheus.NewRegistry()
	m := metrics.NewDispatch(reg, emails.FailureCodes)
	outcome := func(o string) float64 {
		return sample(t, reg, "email_platform_dispatch_total", map[string]string{"outcome": o})
	}
	failures := func(code string) float64 {
		return sample(t, reg, "email_platform_dispatch_failures_total", map[string]string{"error_code": code})
	}
	for _, o := range []string{"handed_off", "failed", "canceled", "retry", "error", "snoozed", "skipped"} {
		if v := outcome(o); v != 0 {
			t.Errorf("outcome %s starts at %v", o, v)
		}
	}
	if n := len(emails.FailureCodes); n == 0 || failures(emails.CodeSuppressed) != 0 {
		t.Fatal("failure codes not exported at zero")
	}

	var behaviour func(context.Context, delivery.Message) (delivery.Result, error)
	p := &fakeProvider{send: func(ctx context.Context, msg delivery.Message) (delivery.Result, error) { return behaviour(ctx, msg) }}
	d := dispatcher(pool, p, nil)
	d.Observe(m, "fake")
	run := func(a jobs.SendEmailArgs, n int) error { return d.SendEmail(ctx, a, attempt(n)) }

	behaviour = captured
	sent := queued(t, pool, projectID)
	if err := run(args(sent), 1); err != nil {
		t.Fatal(err)
	}
	if outcome("handed_off") != 1 || sample(t, reg, "email_platform_provider_send_duration_seconds", map[string]string{"provider": "fake", "result": "ok"}) != 1 {
		t.Errorf("hand-off not counted once")
	}
	// A late retry of the finished email, and a job for an unknown email.
	_ = run(args(sent), 2)
	_ = run(jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: projectID}, 1)
	if outcome("skipped") != 2 || outcome("handed_off") != 1 {
		t.Errorf("skipped = %v, handed_off = %v; want 2, 1", outcome("skipped"), outcome("handed_off"))
	}

	behaviour = func(context.Context, delivery.Message) (delivery.Result, error) {
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassConnect}
	}
	flaky := queued(t, pool, projectID)
	if err := run(args(flaky), 1); err == nil {
		t.Fatal("retryable failure ended the job")
	}
	if err := run(args(flaky), testMaxAttempts); err != nil {
		t.Fatal(err)
	}
	if outcome("retry") != 1 || failures(emails.CodeProviderUnreachable) != 1 ||
		sample(t, reg, "email_platform_provider_send_duration_seconds", map[string]string{"provider": "fake", "result": "error"}) != 2 {
		t.Errorf("retry %v, provider_unreachable %v", outcome("retry"), failures(emails.CodeProviderUnreachable))
	}

	behaviour = func(context.Context, delivery.Message) (delivery.Result, error) {
		return delivery.Result{}, &delivery.Error{Class: delivery.ClassRejected, Code: 550}
	}
	if err := run(args(queued(t, pool, projectID)), 1); err != nil {
		t.Fatal(err)
	}
	if failures(emails.CodeProviderRejected) != 1 || outcome("failed") != 2 {
		t.Errorf("rejected %v, failed %v; want 1, 2", failures(emails.CodeProviderRejected), outcome("failed"))
	}

	// A queued email of a cancelled broadcast ends canceled, unsent.
	calls := p.calls
	if err := run(broadcastEmailIn(t, pool, projectID, "canceled"), 1); err != nil {
		t.Fatal(err)
	}
	if outcome("canceled") != 1 || p.calls != calls {
		t.Errorf("canceled = %v (provider calls %d → %d)", outcome("canceled"), calls, p.calls)
	}

	// The daily cap snoozes the next email (one hand-off within 24 h).
	behaviour = captured
	if err := d.LimitDailyHandoffs("fake", 1); err != nil {
		t.Fatal(err)
	}
	if err := run(args(queued(t, pool, projectID)), 1); err == nil {
		t.Fatal("over the daily cap the job was not snoozed")
	}
	if outcome("snoozed") != 1 {
		t.Errorf("snoozed = %v, want 1", outcome("snoozed"))
	}
	// A bulk (broadcast) email over the cap snoozes on its own branch.
	if err := run(broadcastEmailIn(t, pool, projectID, "sending"), 1); err == nil {
		t.Fatal("bulk email over the daily cap was not snoozed")
	}
	if outcome("snoozed") != 2 {
		t.Errorf("snoozed = %v after the bulk email, want 2", outcome("snoozed"))
	}
	// The provider rate limit snoozes too (one token, the next is 100 s away).
	limited := dispatcher(pool, p, nil)
	limited.Observe(m, "fake")
	if err := limited.LimitProviderRate(0.01); err != nil {
		t.Fatal(err)
	}
	if err := limited.SendEmail(ctx, args(queued(t, pool, projectID)), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if err := limited.SendEmail(ctx, args(queued(t, pool, projectID)), attempt(1)); err == nil {
		t.Fatal("rate-limited send was not snoozed")
	}
	if outcome("snoozed") != 3 {
		t.Errorf("snoozed = %v after the rate limit, want 3", outcome("snoozed"))
	}

	// Expiry by the safety net counts as a failure with code expired.
	stale := queued(t, pool, projectID)
	if _, err := pool.Exec(ctx, "UPDATE emails SET created_at = now() - interval '8 days' WHERE id = $1", stale.ID); err != nil {
		t.Fatal(err)
	}
	before := outcome("failed")
	if err := d.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if failures(emails.CodeExpired) < 1 || outcome("failed") != before {
		t.Errorf("expired = %v; failed outcome %v → %v (expiry is not a job outcome)", failures(emails.CodeExpired), before, outcome("failed"))
	}

	// An internal error on a non-final attempt is an error, not a retry.
	failMarkSending(t, pool)
	retries := outcome("retry")
	d.LimitDailyHandoffs("fake", 1000) //nolint:errcheck // valid limit
	if err := run(args(queued(t, pool, projectID)), 1); err == nil {
		t.Fatal("internal error ended the job")
	}
	if outcome("error") != 1 || outcome("retry") != retries {
		t.Errorf("error = %v, retry %v → %v", outcome("error"), retries, outcome("retry"))
	}
	// On the final attempt the same internal error fails the email.
	if err := run(args(queued(t, pool, projectID)), testMaxAttempts); err != nil {
		t.Fatal(err)
	}
	if failures(emails.CodeInternalError) != 1 {
		t.Errorf("internal_error = %v, want 1", failures(emails.CodeInternalError))
	}
	if t.Failed() {
		families, _ := reg.Gather()
		for _, f := range families {
			t.Log(fmt.Sprint(f))
		}
	}
}

// The provider accepted the message but recording it failed (error), or the
// email was finalized while it was being sent (skipped).
func TestDispatchMetricsAfterTheProviderCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	reg := prometheus.NewRegistry()
	m := metrics.NewDispatch(reg, emails.FailureCodes)
	outcome := func(o string) float64 {
		return sample(t, reg, "email_platform_dispatch_total", map[string]string{"outcome": o})
	}

	var finalize uuid.UUID
	p := &fakeProvider{send: func(ctx context.Context, msg delivery.Message) (delivery.Result, error) {
		if msg.ID == finalize {
			if _, err := pool.Exec(ctx, `UPDATE emails SET status = 'failed', error_code = 'expired', last_error = 'expired', finished_at = now() WHERE id = $1`, msg.ID); err != nil {
				t.Error(err)
			}
		}
		return captured(ctx, msg)
	}}
	d := dispatcher(pool, p, nil)
	d.Observe(m, "fake")

	e := queued(t, pool, projectID)
	finalize = e.ID
	if err := d.SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if outcome("skipped") != 1 || outcome("handed_off") != 0 {
		t.Errorf("finalized during the send: skipped %v, handed_off %v", outcome("skipped"), outcome("handed_off"))
	}

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION refuse_captured() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'injected fault'; END $$;
		CREATE TRIGGER refuse_captured BEFORE UPDATE ON emails
		FOR EACH ROW WHEN (NEW.status = 'captured') EXECUTE FUNCTION refuse_captured();`); err != nil {
		t.Fatal(err)
	}
	if err := d.SendEmail(ctx, args(queued(t, pool, projectID)), attempt(1)); err == nil {
		t.Fatal("an unrecorded hand-off must be retried")
	}
	if outcome("error") != 1 || outcome("handed_off") != 0 {
		t.Errorf("unrecorded hand-off: error %v, handed_off %v", outcome("error"), outcome("handed_off"))
	}
}
