//go:build integration

package emails_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/emails"
)

func TestListPageThatExactlyFillsHasNoMore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	for range 2 {
		if _, err := svc.Create(ctx, projectID, input); err != nil {
			t.Fatal(err)
		}
	}
	page, more, err := svc.List(ctx, projectID, emails.ListFilter{}, 2)
	if err != nil || len(page) != 2 || more {
		t.Fatalf("page %d, more %v, err %v; want 2 emails and no more", len(page), more, err)
	}
	page, more, err = svc.List(ctx, projectID, emails.ListFilter{}, 1)
	if err != nil || len(page) != 1 || !more {
		t.Fatalf("page %d, more %v, err %v; want 1 email and more", len(page), more, err)
	}
}

func TestListCreatedAfterUsesIDBoundWithoutLosingRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	before := time.Now().Add(-time.Second)
	e, err := svc.Create(ctx, projectID, input)
	if err != nil {
		t.Fatal(err)
	}
	// The email is found with created_after up to its exact creation time.
	page, _, err := svc.List(ctx, projectID, emails.ListFilter{CreatedAfter: &e.CreatedAt}, 10)
	if err != nil || len(page) != 1 || page[0].ID != e.ID {
		t.Fatalf("created_after = created_at: %v %v", page, err)
	}
	page, _, err = svc.List(ctx, projectID, emails.ListFilter{CreatedAfter: &before}, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("created_after before: %v %v", page, err)
	}
	later := e.CreatedAt.Add(time.Millisecond)
	if page, _, err = svc.List(ctx, projectID, emails.ListFilter{CreatedAfter: &later}, 10); err != nil || len(page) != 0 {
		t.Fatalf("created_after later: %v %v", page, err)
	}
	// Recipient path, same bound.
	page, _, err = svc.List(ctx, projectID, emails.ListFilter{Recipient: "CUSTOMER@example.test", CreatedAfter: &e.CreatedAt}, 10)
	if err != nil || len(page) != 1 {
		t.Fatalf("recipient + created_after: %v %v", page, err)
	}
	page, _, err = svc.List(ctx, projectID, emails.ListFilter{Recipient: "other@example.test"}, 10)
	if err != nil || len(page) != 0 {
		t.Fatalf("other recipient: %v %v", page, err)
	}
	if _, _, err = svc.List(ctx, uuid.New(), emails.ListFilter{}, 10); err != nil {
		t.Fatal(err)
	}
}

func TestListIsBoundedInConcurrencyAndTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)

	// All slots taken: the request waits, then gives up as busy.
	svc.SetListLimits(1, 50*time.Millisecond, 5*time.Second)
	release := svc.HoldListSlot()
	start := time.Now()
	_, _, err := svc.List(ctx, projectID, emails.ListFilter{}, 10)
	if !errors.Is(err, emails.ErrListBusy) {
		t.Fatalf("busy: err %v, want ErrListBusy", err)
	}
	if waited := time.Since(start); waited < 50*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("waited %v for a slot, want about 50ms", waited)
	}
	release()
	if _, _, err := svc.List(ctx, projectID, emails.ListFilter{}, 10); err != nil {
		t.Fatalf("after release: %v", err)
	}

	// A query running past the timeout is cancelled by the server and
	// reported as busy: a filter that matches nothing scans all of the
	// project's 50 000 emails, far longer than 1 ms.
	if _, err := pool.Exec(ctx, `INSERT INTO emails (project_id, sender, recipient, subject)
		SELECT $1, 'a@example.test', 'b@example.test', 's' FROM generate_series(1, 50000)`, projectID); err != nil {
		t.Fatal(err)
	}
	svc.SetListLimits(1, time.Second, time.Millisecond)
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := svc.List(ctx, projectID, emails.ListFilter{CreatedBefore: &old}, 10); !errors.Is(err, emails.ErrListBusy) {
		t.Fatalf("timeout: err %v, want ErrListBusy", err)
	}
	// The caller's own cancellation is not turned into busy.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	svc.SetListLimits(1, time.Second, 5*time.Second)
	if _, _, err := svc.List(cctx, projectID, emails.ListFilter{}, 10); errors.Is(err, emails.ErrListBusy) || err == nil {
		t.Fatalf("caller cancel: err %v", err)
	}
}
