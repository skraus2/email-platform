//go:build integration

package emails_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

const testMaxAttempts = 5

type failingEnqueuer struct{}

func (failingEnqueuer) EnqueueSendEmail(context.Context, pgx.Tx, jobs.SendEmailArgs) error {
	return errors.New("queue unavailable")
}

func setup(t *testing.T) (*pgxpool.Pool, uuid.UUID) {
	t.Helper()
	pool := testdb.NewMigrated(t)
	p, err := auth.CreateProject(context.Background(), store.New(pool), "app")
	if err != nil {
		t.Fatal(err)
	}
	return pool, p.ID
}

func count(t *testing.T, pool *pgxpool.Pool, query string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var input = emails.CreateInput{
	From: "noreply@example.test", To: "customer@example.test", Subject: "Hi", Text: "Hello", HTML: "<p>Hello</p>",
}

func TestCreatePersistsEmailContentAndJobTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	svc := emails.NewService(pool, enq, slog.New(slog.NewJSONHandler(&logs, nil)))

	e, err := svc.Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != "queued" || e.ProjectID != projectID || e.To != input.To {
		t.Fatalf("email = %+v", e)
	}
	var text, html string
	if err := pool.QueryRow(ctx, "SELECT text_body, html_body FROM email_contents WHERE email_id = $1 AND project_id = $2",
		e.ID, projectID).Scan(&text, &html); err != nil {
		t.Fatal(err)
	}
	if text != input.Text || html != input.HTML {
		t.Errorf("content = %q / %q", text, html)
	}
	var jobArgs string
	if err := pool.QueryRow(ctx, "SELECT args::text FROM river_job WHERE kind = 'send_email'").Scan(&jobArgs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jobArgs, e.ID.String()) || !strings.Contains(jobArgs, projectID.String()) {
		t.Errorf("job args = %s", jobArgs)
	}
	if strings.Contains(logs.String(), "customer@example.test") || strings.Contains(logs.String(), "Hello") {
		t.Errorf("log contains recipient or body: %s", logs.String())
	}
}

func TestCreateIsAtomicWhenEnqueueFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := emails.NewService(pool, failingEnqueuer{}, slog.New(slog.DiscardHandler))

	if _, err := svc.Create(ctx, projectID, input); err == nil {
		t.Fatal("Create succeeded although the job could not be enqueued")
	}
	if n := count(t, pool, "SELECT count(*) FROM emails"); n != 0 {
		t.Fatalf("emails rows = %d after failed enqueue, want 0", n)
	}
	if n := count(t, pool, "SELECT count(*) FROM email_contents"); n != 0 {
		t.Fatalf("content rows = %d after failed enqueue, want 0", n)
	}
}

func TestCreateRejectsInvalidInputWithoutWriting(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	svc := emails.NewService(pool, failingEnqueuer{}, slog.New(slog.DiscardHandler))
	bad := input
	bad.Subject = "Hi\r\nBcc: x@example.test"
	var ve *emails.ValidationError
	if _, err := svc.Create(context.Background(), projectID, bad); !errors.As(err, &ve) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
	if n := count(t, pool, "SELECT count(*) FROM emails"); n != 0 {
		t.Fatalf("emails rows = %d", n)
	}
}

func TestContentProjectMustMatchEmailProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	other, _ := auth.CreateProject(ctx, store.New(pool), "other")
	q := store.New(pool)
	e, err := q.InsertEmail(ctx, store.InsertEmailParams{ProjectID: projectID, Sender: "a@example.test", Recipient: "b@example.test", Subject: "s"})
	if err != nil {
		t.Fatal(err)
	}
	text := "x"
	err = q.InsertEmailContent(ctx, store.InsertEmailContentParams{EmailID: e.ID, ProjectID: other.ID, TextBody: &text})
	if err == nil {
		t.Fatal("content attached to an email of another project")
	}
}

type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueSendEmail(context.Context, pgx.Tx, jobs.SendEmailArgs) error { return nil }
