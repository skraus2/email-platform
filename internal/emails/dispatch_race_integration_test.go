//go:build integration

package emails

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

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
