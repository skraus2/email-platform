//go:build integration

package emails

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

const automationUnsubscribeURL = "https://app.example.test/u?t=tok"

// automationEmail stores a queued automation email (with its template,
// contact, automation and run, and its content with a List-Unsubscribe URL)
// and returns the email, contact, run and automation ids.
func automationEmail(t *testing.T, ctx context.Context, pool *pgxpool.Pool, projectID uuid.UUID) (emailID, contactID, runID, automationID uuid.UUID) {
	t.Helper()
	row := func(dst *uuid.UUID, sql string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	var templateID, version uuid.UUID
	row(&templateID, "INSERT INTO templates (project_id, slug, latest_version) VALUES ($1, 'welcome', 1) RETURNING id", projectID)
	row(&version, `INSERT INTO template_versions (template_id, project_id, version, subject, text_body, variables)
		VALUES ($1, $2, 1, 'Welcome', 'Bye {{.unsubscribe_url}}', '{"unsubscribe_url":{"type":"string","required":false,"max_length":1000}}')
		RETURNING template_id`, templateID, projectID)
	row(&contactID, "INSERT INTO contacts (project_id, email, status, consent_source, unsubscribe_token) VALUES ($1, 'customer@example.test', 'subscribed', 'x', $2) RETURNING id",
		projectID, "tok_"+strings.Repeat("a", 39))
	row(&automationID, `INSERT INTO automations (project_id, name, status, trigger_event, steps)
		VALUES ($1, 'Welcome', 'active', 'user.signed_up', '[{"type":"send"}]') RETURNING id`, projectID)
	row(&runID, `INSERT INTO automation_runs (project_id, automation_id, contact_id, step, next_step_at)
		VALUES ($1, $2, $3, 1, now() + interval '1 day') RETURNING id`, projectID, automationID, contactID)
	row(&emailID, `INSERT INTO emails (project_id, sender, recipient, subject, template_id, template_version, contact_id,
		automation_id, automation_run_id, automation_step)
		VALUES ($1, 'noreply@example.test', 'customer@example.test', 'Welcome', $2, 1, $3, $4, $5, 0) RETURNING id`,
		projectID, templateID, contactID, automationID, runID)
	if _, err := pool.Exec(ctx, `INSERT INTO email_contents (email_id, project_id, text_body, list_unsubscribe_url) VALUES ($1, $2, 'Bye', $3)`,
		emailID, projectID, automationUnsubscribeURL); err != nil {
		t.Fatal(err)
	}
	return emailID, contactID, runID, automationID
}

type messageProvider struct {
	mu   sync.Mutex
	sent []delivery.Message
}

func (p *messageProvider) Send(_ context.Context, m delivery.Message) (delivery.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, m)
	return delivery.Result{Provider: "fake", ProviderID: "<x@y>", Status: delivery.StatusCaptured}, nil
}

func newAutomationProject(t *testing.T) (context.Context, *pgxpool.Pool, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	p, err := auth.CreateProject(ctx, store.New(pool), "app")
	if err != nil {
		t.Fatal(err)
	}
	return ctx, pool, p.ID
}

func emailState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (string, *string, bool) {
	t.Helper()
	var status string
	var code *string
	var content bool
	if err := pool.QueryRow(ctx, `SELECT status, error_code, EXISTS (SELECT 1 FROM email_contents WHERE email_id = $1) FROM emails WHERE id = $1`, id).
		Scan(&status, &code, &content); err != nil {
		t.Fatal(err)
	}
	return status, code, content
}

// An automation email is sent from its stored content with its personal
// List-Unsubscribe URL; the content is deleted afterwards.
func TestAutomationEmailSendsStoredContent(t *testing.T) {
	t.Parallel()
	ctx, pool, project := newAutomationProject(t)
	emailID, _, _, _ := automationEmail(t, ctx, pool, project)
	prov := &messageProvider{}
	d := NewDispatcher(pool, prov, slog.New(slog.DiscardHandler), 5*time.Second, time.Hour)
	if err := d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: emailID, ProjectID: project}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	if len(prov.sent) != 1 || prov.sent[0].Text != "Bye" || prov.sent[0].ListUnsubscribeURL != automationUnsubscribeURL || prov.sent[0].Subject != "Welcome" {
		t.Fatalf("sent: %+v", prov.sent)
	}
	if status, code, content := emailState(t, ctx, pool, emailID); status != "captured" || code != nil || content {
		t.Fatalf("after send: %s %v content=%v", status, code, content)
	}
}

// Each refusal reason, present when the email is loaded, ends it without a
// provider call.
func TestAutomationEmailRechecksAtSend(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		change  string
		status  string
		code    string
		sending bool
	}{
		"run canceled":               {"UPDATE automation_runs SET status = 'canceled', reason = 'canceled', next_step_at = NULL, finished_at = now() WHERE id = $2 AND $1::uuid IS NOT NULL AND $3::uuid IS NOT NULL", "canceled", "", false},
		"run canceled while sending": {"UPDATE automation_runs SET status = 'canceled', reason = 'canceled', next_step_at = NULL, finished_at = now() WHERE id = $2 AND $1::uuid IS NOT NULL AND $3::uuid IS NOT NULL", "failed", CodeAutomationCanceled, true},
		"automation archived":        {"UPDATE automations SET status = 'archived', archived_at = now() WHERE id = $3 AND $1::uuid IS NOT NULL AND $2::uuid IS NOT NULL", "canceled", "", false},
		"contact unsubscribed":       {"UPDATE contacts SET status = 'unsubscribed' WHERE id = $1 AND $2::uuid IS NOT NULL AND $3::uuid IS NOT NULL", "failed", CodeUnsubscribed, false},
		"contact deleted":            {"DELETE FROM contacts WHERE id = $1 AND $2::uuid IS NOT NULL AND $3::uuid IS NOT NULL", "failed", CodeContactDeleted, false},
		// A run deleted by retention is no reason to stop the email.
		"run deleted": {"DELETE FROM automation_runs WHERE id = $2 AND $1::uuid IS NOT NULL AND $3::uuid IS NOT NULL", "captured", "", false},
	} {
		for _, when := range []string{"before load", "between load and claim"} {
			t.Run(name+"/"+when, func(t *testing.T) {
				t.Parallel()
				ctx, pool, project := newAutomationProject(t)
				emailID, contactID, runID, automationID := automationEmail(t, ctx, pool, project)
				if c.sending {
					if _, err := pool.Exec(ctx, "UPDATE emails SET status = 'sending', attempts = 1 WHERE id = $1", emailID); err != nil {
						t.Fatal(err)
					}
				}
				change := func() {
					if _, err := pool.Exec(ctx, c.change, contactID, runID, automationID); err != nil {
						t.Error(err)
					}
				}
				prov := &countingProvider{}
				d := NewDispatcher(pool, prov, slog.New(slog.DiscardHandler), 5*time.Second, time.Hour)
				if when == "before load" {
					change()
				} else {
					var once sync.Once
					d.limiter = newBucket(100, func() time.Time {
						once.Do(change)
						return time.Now()
					})
				}
				if err := d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: emailID, ProjectID: project}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5}); err != nil {
					t.Fatal(err)
				}
				status, code, content := emailState(t, ctx, pool, emailID)
				wantCalls := 0
				if c.status == "captured" {
					wantCalls = 1
				}
				if prov.calls != wantCalls || status != c.status || (c.code == "" && code != nil) || (c.code != "" && (code == nil || *code != c.code)) || content {
					t.Fatalf("calls=%d status=%s code=%v content=%v, want %d/%s/%q", prov.calls, status, code, content, wantCalls, c.status, c.code)
				}
				if c.status == "canceled" {
					var events int
					if err := pool.QueryRow(ctx, "SELECT count(*) FROM email_events WHERE email_id = $1 AND type = 'canceled'", emailID).Scan(&events); err != nil || events != 1 {
						t.Fatalf("canceled events: %d %v", events, err)
					}
				}
			})
		}
	}
}

// Automation emails use the bulk share of the daily cap.
func TestAutomationEmailUsesBulkShareOfDailyCap(t *testing.T) {
	t.Parallel()
	ctx, pool, project := newAutomationProject(t)
	emailID, _, _, _ := automationEmail(t, ctx, pool, project)
	for range 9 {
		if _, err := pool.Exec(ctx, `INSERT INTO emails (project_id, sender, recipient, subject, status, provider, provider_id, finished_at)
			VALUES ($1, 'a@example.test', 'b@example.test', 's', 'captured', 'fake', 'x', now())`, project); err != nil {
			t.Fatal(err)
		}
	}
	prov := &countingProvider{}
	d := NewDispatcher(pool, prov, slog.New(slog.DiscardHandler), 5*time.Second, time.Hour)
	if err := d.LimitDailyHandoffs("fake", 10); err != nil {
		t.Fatal(err)
	}
	err := d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: emailID, ProjectID: project}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5})
	if wait, ok := jobs.SnoozeDelay(err); !ok || prov.calls != 0 || wait < time.Minute {
		t.Fatalf("automation email at 9/10: %v, calls %d", err, prov.calls)
	}
}
