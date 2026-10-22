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
	"github.com/jackc/pgx/v5"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

type countingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingProvider) Send(context.Context, delivery.Message) (delivery.Result, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return delivery.Result{Provider: "fake", ProviderID: "<x@y>", Status: delivery.StatusCaptured}, nil
}

// A change made between loading the email and claiming it (here: during the
// provider-rate check, which sits exactly there) must still stop the send.
func TestChangeBetweenLoadAndClaimStopsSend(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]string{
		"suppressed":       "INSERT INTO suppressions (project_id, recipient, reason) VALUES ($1, 'customer@example.test', 'manual')",
		"project disabled": "UPDATE projects SET status = 'disabled' WHERE id = $1",
		"canceled":         "UPDATE emails SET status = 'canceled', finished_at = now() WHERE project_id = $1",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := testdb.NewMigrated(t)
			q := store.New(pool)
			p, err := auth.CreateProject(ctx, q, "app")
			if err != nil {
				t.Fatal(err)
			}
			e, err := q.InsertEmail(ctx, store.InsertEmailParams{ProjectID: p.ID, Sender: "noreply@example.test", Recipient: "customer@example.test", Subject: "Hi"})
			if err != nil {
				t.Fatal(err)
			}
			text := "Hello"
			if err := q.InsertEmailContent(ctx, store.InsertEmailContentParams{EmailID: e.ID, ProjectID: p.ID, TextBody: &text}); err != nil {
				t.Fatal(err)
			}

			prov := &countingProvider{}
			d := NewDispatcher(pool, prov, slog.New(slog.DiscardHandler), 5*time.Second, time.Hour)
			var once sync.Once
			d.limiter = newBucket(100, func() time.Time {
				once.Do(func() {
					if _, err := pool.Exec(ctx, change, p.ID); err != nil {
						t.Error(err)
					}
				})
				return time.Now()
			})
			if err := d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: e.ID, ProjectID: p.ID}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5}); err != nil {
				t.Fatal(err)
			}
			var status string
			var code *string
			var hasContent bool
			if err := pool.QueryRow(ctx, `SELECT status, error_code, EXISTS (SELECT 1 FROM email_contents WHERE email_id = $1)
				FROM emails WHERE id = $1`, e.ID).Scan(&status, &code, &hasContent); err != nil {
				t.Fatal(err)
			}
			if name == "canceled" {
				// The cancel won: the claim finds nothing to send and changes nothing.
				if prov.calls != 0 || status != "canceled" || code != nil {
					t.Fatalf("calls=%d status=%s code=%v, want canceled without sending", prov.calls, status, code)
				}
				return
			}
			want := map[string]string{"suppressed": CodeSuppressed, "project disabled": CodeProjectDisabled}[name]
			if prov.calls != 0 || status != "failed" || code == nil || *code != want || hasContent {
				t.Fatalf("calls=%d status=%s code=%v content=%v, want failed/%s without sending", prov.calls, status, code, hasContent, want)
			}
		})
	}
}

// broadcastEmail stores a broadcast email (with its template, contact,
// audience and broadcast) and returns the email, contact and broadcast ids.
func broadcastEmail(t *testing.T, ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, projectID uuid.UUID) (emailID, contactID, broadcastID uuid.UUID) {
	t.Helper()
	row := func(dst *uuid.UUID, sql string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	var templateID, audienceID, version uuid.UUID
	row(&templateID, "INSERT INTO templates (project_id, slug, latest_version) VALUES ($1, 'news', 1) RETURNING id", projectID)
	row(&version, `INSERT INTO template_versions (template_id, project_id, version, subject, text_body, variables)
		VALUES ($1, $2, 1, 'News', 'Bye {{.unsubscribe_url}}', '{"unsubscribe_url":{"type":"string","required":false,"max_length":1000}}')
		RETURNING template_id`, templateID, projectID)
	row(&contactID, "INSERT INTO contacts (project_id, email, status, consent_source, unsubscribe_token) VALUES ($1, 'customer@example.test', 'subscribed', 'x', $2) RETURNING id",
		projectID, "tok_"+strings.Repeat("a", 39))
	row(&audienceID, "INSERT INTO audiences (project_id, name) VALUES ($1, 'list') RETURNING id", projectID)
	row(&broadcastID, `INSERT INTO broadcasts (project_id, audience_id, sender, template_id, template_version, unsubscribe_url, status, scheduled_at, started_at)
		VALUES ($1, $2, 'noreply@example.test', $3, 1, 'https://app.example.test/u?t={token}', 'sending', now(), now()) RETURNING id`,
		projectID, audienceID, templateID)
	row(&emailID, `INSERT INTO emails (project_id, sender, recipient, subject, template_id, template_version, broadcast_id, contact_id)
		VALUES ($1, 'noreply@example.test', 'customer@example.test', 'News', $2, 1, $3, $4) RETURNING id`,
		projectID, templateID, broadcastID, contactID)
	return emailID, contactID, broadcastID
}

// The broadcast and contact conditions are re-checked in the claim, so a
// change made between loading a broadcast email and claiming it stops the
// send with the same outcome as one made before the load.
func TestBroadcastChangeBetweenLoadAndClaimStopsSend(t *testing.T) {
	t.Parallel()
	cancel := "UPDATE broadcasts SET status = 'canceled', finished_at = now() WHERE id = $2 AND $1::uuid IS NOT NULL"
	for name, c := range map[string]struct {
		change  string
		status  string
		code    string
		sending bool // an earlier attempt left the email "sending"
	}{
		"broadcast canceled":               {cancel, "canceled", "", false},
		"broadcast canceled while sending": {cancel, "failed", CodeBroadcastCanceled, true},
		"contact unsubscribed":             {"UPDATE contacts SET status = 'unsubscribed' WHERE id = $1 AND $2::uuid IS NOT NULL", "failed", CodeUnsubscribed, false},
		"contact deleted":                  {"DELETE FROM contacts WHERE id = $1 AND $2::uuid IS NOT NULL", "failed", CodeContactDeleted, false},
		"no change":                        {"SELECT $1::uuid, $2::uuid", "captured", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := testdb.NewMigrated(t)
			q := store.New(pool)
			p, err := auth.CreateProject(ctx, q, "app")
			if err != nil {
				t.Fatal(err)
			}
			emailID, contactID, broadcastID := broadcastEmail(t, ctx, pool, p.ID)
			if c.sending {
				if _, err := pool.Exec(ctx, "UPDATE emails SET status = 'sending', attempts = 1 WHERE id = $1", emailID); err != nil {
					t.Fatal(err)
				}
			}
			prov := &countingProvider{}
			d := NewDispatcher(pool, prov, slog.New(slog.DiscardHandler), 5*time.Second, time.Hour)
			var once sync.Once
			d.limiter = newBucket(100, func() time.Time {
				once.Do(func() {
					if _, err := pool.Exec(ctx, c.change, contactID, broadcastID); err != nil {
						t.Error(err)
					}
				})
				return time.Now()
			})
			if err := d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: emailID, ProjectID: p.ID}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5}); err != nil {
				t.Fatal(err)
			}
			var status string
			var code *string
			if err := pool.QueryRow(ctx, "SELECT status, error_code FROM emails WHERE id = $1", emailID).Scan(&status, &code); err != nil {
				t.Fatal(err)
			}
			wantCalls := 0
			if c.status == "captured" {
				wantCalls = 1
			}
			if prov.calls != wantCalls || status != c.status || (c.code == "" && code != nil) || (c.code != "" && (code == nil || *code != c.code)) {
				t.Fatalf("calls=%d status=%s code=%v, want %d/%s/%q", prov.calls, status, code, wantCalls, c.status, c.code)
			}
		})
	}
}

// Broadcast emails stop at 90 % of the daily hand-off cap, so the rest
// stays available for transactional mail.
func TestDailyCapReserveForTransactionalMail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	p, err := auth.CreateProject(ctx, q, "app")
	if err != nil {
		t.Fatal(err)
	}
	bulk, _, _ := broadcastEmail(t, ctx, pool, p.ID)
	for range 9 {
		if _, err := pool.Exec(ctx, `INSERT INTO emails (project_id, sender, recipient, subject, status, provider, provider_id, finished_at)
			VALUES ($1, 'a@example.test', 'b@example.test', 's', 'captured', 'fake', 'x', now())`, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := q.InsertEmail(ctx, store.InsertEmailParams{ProjectID: p.ID, Sender: "noreply@example.test", Recipient: "user@example.test", Subject: "Reset"})
	if err != nil {
		t.Fatal(err)
	}
	text := "x"
	if err := q.InsertEmailContent(ctx, store.InsertEmailContentParams{EmailID: tx.ID, ProjectID: p.ID, TextBody: &text}); err != nil {
		t.Fatal(err)
	}
	prov := &countingProvider{}
	d := NewDispatcher(pool, prov, slog.New(slog.DiscardHandler), 5*time.Second, time.Hour)
	if err := d.LimitDailyHandoffs("fake", 10); err != nil {
		t.Fatal(err)
	}
	err = d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: bulk, ProjectID: p.ID}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5})
	// A broadcast backlog waits at least a minute, spread over a window.
	if wait, ok := jobs.SnoozeDelay(err); !ok || prov.calls != 0 || wait < time.Minute {
		t.Fatalf("broadcast email at 9/10: %v, calls %d", err, prov.calls)
	}
	if err := d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: tx.ID, ProjectID: p.ID}, jobs.Attempt{JobID: 2, Number: 1, MaxAttempts: 5}); err != nil || prov.calls != 1 {
		t.Fatalf("transactional email at 9/10: %v, calls %d", err, prov.calls)
	}
	// A cap of 1 leaves broadcasts no share: they wait an hour, not a second.
	if err := d.LimitDailyHandoffs("fake", 1); err != nil {
		t.Fatal(err)
	}
	err = d.SendEmail(ctx, jobs.SendEmailArgs{EmailID: bulk, ProjectID: p.ID}, jobs.Attempt{JobID: 1, Number: 1, MaxAttempts: 5})
	if wait, ok := jobs.SnoozeDelay(err); !ok || wait < time.Hour {
		t.Fatalf("broadcast email at cap 1: %v", err)
	}
}
