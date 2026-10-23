//go:build integration

package metrics_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/metrics"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

func TestBacklogGauges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	p, err := auth.CreateProject(ctx, store.New(pool), "app")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO emails (project_id, sender, recipient, subject, status, created_at) VALUES
		($1, 'a@example.test', 'u@example.test', 's', 'queued', now() - interval '1 minute'),
		($1, 'a@example.test', 'u@example.test', 's', 'sending', now())`, p.ID)
	exec(`INSERT INTO emails (project_id, sender, recipient, subject, status, scheduled_at) VALUES
		($1, 'a@example.test', 'u@example.test', 's', 'queued', now() + interval '1 hour')`, p.ID) // not due
	exec(`INSERT INTO emails (project_id, sender, recipient, subject, status, provider, provider_id, created_at, finished_at) VALUES
		($1, 'a@example.test', 'u@example.test', 's', 'captured', 'mailpit', 'm', now(), now()),
		($1, 'a@example.test', 'u@example.test', 's', 'captured', 'mailpit', 'm', now() - interval '2 days', now() - interval '2 days')`, p.ID)
	var emailID, eventID, endpointID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM emails WHERE status = 'sending'`).Scan(&emailID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO email_events (project_id, email_id, origin, type, occurred_at) VALUES ($1, $2, 'platform', 'captured', now()) RETURNING id`,
		p.ID, emailID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO webhook_endpoints (id, project_id, url, event_types, secret_ciphertext, secret_key_id)
		VALUES ($1, $2, 'https://hooks.example.test/', '{email.captured}', $3, 'k1') RETURNING id`, uuid.New(), p.ID, make([]byte, 64)).Scan(&endpointID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO webhook_deliveries (project_id, endpoint_id, event_id, created_at) VALUES ($1, $2, $3, now() - interval '20 minutes')`, p.ID, endpointID, eventID)

	enq, err := jobs.NewEnqueuer(pool, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, bulk := range []bool{false, true, true} {
		if err := enq.EnqueueSendEmail(ctx, tx, jobs.SendEmailArgs{EmailID: uuid.New(), ProjectID: p.ID}, jobs.SendOpts{Bulk: bulk}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE river_job SET scheduled_at = now() - interval '90 seconds' WHERE priority = 1`)

	reg := prometheus.NewRegistry()
	metrics.RegisterBacklog(reg, pool, "mailpit", 500)
	got := series(t, reg, "email_platform_")
	for _, want := range []string{
		"email_platform_db_up{} 1",
		"email_platform_emails_open{status=queued} 1",
		"email_platform_emails_open{status=sending} 1",
		"email_platform_queue_jobs{queue=default,state=available} 3",
		"email_platform_queue_jobs{queue=webhooks,state=available} 0",
		"email_platform_webhook_pending{} 1",
		"email_platform_provider_handoffs_24h{provider=mailpit} 1",
		"email_platform_provider_daily_limit{provider=mailpit} 500",
		"email_platform_queue_oldest_available_seconds{class=transactional,queue=webhooks} 0",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	lag := func(class string) float64 {
		families, _ := reg.Gather()
		for _, f := range families {
			if f.GetName() != "email_platform_queue_oldest_available_seconds" {
				continue
			}
			for _, m := range f.GetMetric() {
				if m.GetLabel()[0].GetValue() == class && m.GetLabel()[1].GetValue() == "default" {
					return m.GetGauge().GetValue()
				}
			}
		}
		return -1
	}
	if l := lag("transactional"); l < 85 || l > 300 {
		t.Errorf("transactional lag %v, want ≈ 90 s", l)
	}
	if l := lag("bulk"); l < 0 || l > 30 {
		t.Errorf("bulk lag %v, want ≈ 0 s", l)
	}
	if oldest := gaugeValue(t, reg, "email_platform_webhook_oldest_pending_seconds"); oldest < 1100 || oldest > 1400 {
		t.Errorf("webhook oldest %v, want ≈ 1200 s", oldest)
	}

	// Within the cache window a second scrape reuses the snapshot.
	exec(`UPDATE emails SET status = 'queued' WHERE status = 'sending'`)
	if again := series(t, reg, "email_platform_emails_open"); !slices.Contains(again, "email_platform_emails_open{status=queued} 1") {
		t.Errorf("snapshot not reused: %q", again)
	}

	// A database that is gone gives db_up 0 and no stale backlog values.
	down := testdb.NewMigrated(t)
	regDown := prometheus.NewRegistry()
	metrics.RegisterBacklog(regDown, down, "mailpit", 0)
	down.Close()
	start := time.Now()
	got = series(t, regDown, "email_platform_")
	if !slices.Equal(got, []string{"email_platform_db_up{} 0", "email_platform_provider_daily_limit{provider=mailpit} 0"}) {
		t.Errorf("with the database down: %q", got)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("scrape took %v with the database down", time.Since(start))
	}
}

func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("no %s", name)
	return 0
}
