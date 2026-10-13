//go:build integration

package httpapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/testmailpit"
)

// TestSendThroughWorkerToMailpit is the end-to-end acceptance path:
// POST /v1/emails -> persisted + queued -> worker -> Mailpit -> status captured.
func TestSendThroughWorkerToMailpit(t *testing.T) {
	t.Parallel()
	s := newStack(t)
	_, key := s.project(t)

	mp, err := delivery.NewMailpit(testmailpit.SMTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	d := emails.NewDispatcher(s.pool, mp, logger, 5*time.Second, 168*time.Hour)
	w, err := jobs.NewWorker(jobs.WorkerConfig{
		Pool: s.pool, Logger: logger, Concurrency: 2, JobTimeout: 30 * time.Second,
		Handlers: jobs.Handlers{SendEmail: d.SendEmail, Cleanup: d.Cleanup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Stop(5 * time.Second) })

	subject := "E2E " + t.Name() + " " + time.Now().Format(time.RFC3339Nano)
	body := `{"from":"noreply@example.test","to":"customer@example.test","subject":"` + subject +
		`","text":"Plain text body","html":"<p>HTML <b>body</b></p>"}`
	code, created := s.do(t, http.MethodPost, "/v1/emails", key, body)
	if code != http.StatusAccepted || created["status"] != "queued" {
		t.Fatalf("POST = %d %v", code, created)
	}
	id := created["id"].(string)

	var status string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, e := s.do(t, http.MethodGet, "/v1/emails/"+id, key, "")
		status, _ = e["status"].(string)
		if status == "captured" || status == "failed" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "captured" {
		t.Fatalf("status = %q, want captured (never delivered for Mailpit)", status)
	}

	got := testmailpit.FindByMessageID(t, id+"@email-platform.local", 5*time.Second)
	if len(got) != 1 {
		t.Fatalf("Mailpit has %d messages for this email, want exactly 1", len(got))
	}
	m := got[0]
	if m.From.Address != "noreply@example.test" || len(m.To) != 1 || m.To[0].Address != "customer@example.test" ||
		m.Subject != subject || strings.TrimSpace(m.Text) != "Plain text body" || strings.TrimSpace(m.HTML) != "<p>HTML <b>body</b></p>" {
		t.Fatalf("captured message differs: %+v", m)
	}

	var contents int
	_ = s.pool.QueryRow(context.Background(), "SELECT count(*) FROM email_contents WHERE email_id = $1", id).Scan(&contents)
	if contents != 0 {
		t.Fatal("content not deleted after the email was captured")
	}
}
