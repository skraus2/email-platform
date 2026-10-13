//go:build integration

package emails_test

import (
	"context"
	"testing"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/emails"
)

// TestErrorCodeMigrationBackfill migrates a database with Phase-1-style
// rows down and up again across the error_code migration and checks the
// derived codes; last_error itself must stay untouched.
func TestErrorCodeMigrationBackfill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	m, err := database.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.DownTo(ctx, 5); err != nil {
		t.Fatal(err)
	}

	rows := []struct{ status, lastError, want string }{
		{"queued", "delivery failed: connect", "provider_unreachable"},
		{"sending", "delivery failed: timeout", "provider_timeout"},
		{"sending", "delivery failed: protocol", "provider_protocol_error"},
		{"queued", "delivery failed: rejected (smtp 451)", "provider_deferred"},
		{"failed", "delivery failed: rejected (smtp 550)", "provider_rejected"},
		{"failed", "delivery failed: rejected", "provider_rejected"},
		{"failed", "delivery failed: invalid", "message_invalid"},
		{"failed", "delivery failed: unsafe_target", "unsafe_target"},
		{"failed", "project_disabled", "project_disabled"},
		{"failed", "content_missing", "content_missing"},
		{"failed", "expired", "expired"},
		{"failed", "delivery failed: internal", "internal_error"},
		{"failed", "something unexpected", "internal_error"},
		{"failed", "", "internal_error"}, // failed without a reason
		{"queued", "", ""},
		{"captured", "", ""},
	}
	for i, r := range rows {
		var lastError any
		if r.lastError != "" {
			lastError = r.lastError
		}
		finished := r.status == "failed" || r.status == "captured"
		if _, err := pool.Exec(ctx, `INSERT INTO emails (project_id, sender, recipient, subject, status, last_error, finished_at)
			VALUES ($1, 'a@example.test', 'b@example.test', $2, $3, $4, CASE WHEN $5 THEN now() END)`,
			projectID, string(rune('A'+i)), r.status, lastError, finished); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		var code, lastError *string
		if err := pool.QueryRow(ctx, "SELECT error_code, last_error FROM emails WHERE subject = $1",
			string(rune('A'+i))).Scan(&code, &lastError); err != nil {
			t.Fatal(err)
		}
		got := ""
		if code != nil {
			got = *code
		}
		if got != r.want {
			t.Errorf("%s / %q: error_code = %q, want %q", r.status, r.lastError, got, r.want)
		}
		if (lastError == nil) != (r.lastError == "") || (lastError != nil && *lastError != r.lastError) {
			t.Errorf("%s / %q: last_error changed to %v", r.status, r.lastError, lastError)
		}
	}
}

// TestErrorCodeConstraints: every code the application can write is
// accepted, anything else is refused, and the status invariants hold.
func TestErrorCodeConstraints(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	insert := func(status string, code any) error {
		_, err := pool.Exec(ctx, `INSERT INTO emails (project_id, sender, recipient, subject, status, error_code, finished_at)
			VALUES ($1, 'a@example.test', 'b@example.test', 's', $2, $3, CASE WHEN $2 IN ('failed','captured') THEN now() END)`,
			projectID, status, code)
		return err
	}
	for _, c := range emails.FailureCodes {
		if err := insert("failed", c); err != nil {
			t.Errorf("code %q refused by the database: %v", c, err)
		}
	}
	for name, tc := range map[string]struct {
		status string
		code   any
	}{
		"unknown code":         {"failed", "delivery failed: connect"},
		"failed without code":  {"failed", nil},
		"captured with a code": {"captured", "provider_timeout"},
	} {
		if err := insert(tc.status, tc.code); err == nil {
			t.Errorf("%s: accepted, want CHECK violation", name)
		}
	}
	if err := insert("queued", "provider_unreachable"); err != nil {
		t.Errorf("queued with a retry reason refused: %v", err)
	}
}
