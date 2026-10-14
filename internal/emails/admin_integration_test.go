//go:build integration

package emails_test

import (
	"context"
	"strings"
	"testing"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/store"
)

func TestListEmailsForAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	q := store.New(pool)
	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, pool, nil)
	first, err := svc.Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, other.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE emails SET status = 'failed', attempts = 2, error_code = 'provider_rejected',
		last_error = 'delivery failed: rejected (smtp 550)', finished_at = now() WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}

	all, err := emails.ListEmails(ctx, q, projectID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != second.ID || all[1].ID != first.ID {
		t.Fatalf("list = %+v, want this project's two emails, newest first", all)
	}
	for _, e := range all {
		if e.Recipient == input.To || !strings.Contains(e.Recipient, "***") {
			t.Errorf("recipient %q is not masked", e.Recipient)
		}
	}

	failed, err := emails.ListEmails(ctx, q, projectID, "failed", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].ID != first.ID || failed[0].Attempts != 2 ||
		failed[0].ErrorCode == nil || *failed[0].ErrorCode != "provider_rejected" ||
		failed[0].Detail == nil || *failed[0].Detail != "delivery failed: rejected (smtp 550)" {
		t.Fatalf("failed list = %+v", failed)
	}

	one, err := emails.ListEmails(ctx, q, projectID, "", 1)
	if err != nil || len(one) != 1 {
		t.Fatalf("limit 1: %d rows, %v", len(one), err)
	}

	for _, bad := range []struct {
		status string
		limit  int
	}{{"opened", 10}, {"", -1}, {"", emails.MaxAdminListLimit + 1}} {
		if _, err := emails.ListEmails(ctx, q, projectID, bad.status, bad.limit); err == nil {
			t.Errorf("status %q limit %d accepted", bad.status, bad.limit)
		}
	}
}
