//go:build integration

package emails_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/store"
)

func TestSuppressionAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	q := store.New(pool)

	added, err := emails.AddSuppression(ctx, q, projectID, "Customer@Example.test", "manual")
	if err != nil || !added {
		t.Fatalf("add = %v, %v", added, err)
	}
	// Same address in another case: already suppressed.
	if added, err := emails.AddSuppression(ctx, q, projectID, "customer@example.test", "bounce"); err != nil || added {
		t.Fatalf("second add = %v, %v; want existing", added, err)
	}
	list, err := emails.ListSuppressions(ctx, q, projectID, 0)
	if err != nil || len(list) != 1 || list[0].Recipient != "Customer@Example.test" || list[0].Reason != "manual" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	for name, tc := range map[string]struct{ addr, reason string }{
		"display name":   {"Name <a@example.test>", "manual"},
		"whitespace":     {" a@example.test", "manual"},
		"line break":     {"a@example.test\r\n", "manual"},
		"not an email":   {"nope", "manual"},
		"unknown reason": {"a@example.test", "spam"},
	} {
		if _, err := emails.AddSuppression(ctx, q, projectID, tc.addr, tc.reason); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.AddSuppression(ctx, q, other.ID, "customer@example.test", "bounce"); err != nil {
		t.Fatal(err)
	}

	removed, err := emails.RemoveSuppression(ctx, q, projectID, "CUSTOMER@example.test")
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if removed, err := emails.RemoveSuppression(ctx, q, projectID, "customer@example.test"); err != nil || removed {
		t.Fatalf("second remove = %v, %v; want nothing to remove", removed, err)
	}
	if list, err := emails.ListSuppressions(ctx, q, other.ID, 0); err != nil || len(list) != 1 {
		t.Fatalf("other project's suppression = %+v, %v; removing in one project must not touch another", list, err)
	}
}

func TestSuppressedRecipientIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	q := store.New(pool)
	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.AddSuppression(ctx, q, projectID, "CUSTOMER@example.test", "complaint"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	svc := newService(t, pool, &logs)

	if _, err := svc.Create(ctx, projectID, input); !errors.Is(err, emails.ErrRecipientSuppressed) {
		t.Fatalf("err = %v, want ErrRecipientSuppressed", err)
	}
	if _, err := svc.Create(ctx, projectID, keyed("k1")); !errors.Is(err, emails.ErrRecipientSuppressed) {
		t.Fatalf("keyed: err = %v, want ErrRecipientSuppressed", err)
	}
	if got := counts(t, pool); got != (rowCounts{}) {
		t.Fatalf("rows = %+v, want nothing stored", got)
	}
	if strings.Contains(logs.String(), "customer@example.test") {
		t.Error("recipient logged unmasked")
	}

	// Another project is not affected.
	if _, err := svc.Create(ctx, other.ID, input); err != nil {
		t.Fatalf("other project refused: %v", err)
	}
}

// An idempotent replay of an email accepted before the suppression still
// returns that email: the request was accepted then.
func TestReplayAfterSuppression(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	svc := newService(t, pool, nil)
	first, err := svc.Create(ctx, projectID, keyed("k1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.AddSuppression(ctx, store.New(pool), projectID, input.To, "bounce"); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Create(ctx, projectID, keyed("k1"))
	if err != nil || !again.Replayed || again.ID != first.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
}

func TestDispatchRefusesRecipientSuppressedAfterQueueing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	e := queued(t, pool, projectID)
	other, err := auth.CreateProject(ctx, store.New(pool), "other")
	if err != nil {
		t.Fatal(err)
	}
	eOther := queued(t, pool, other.ID)
	if _, err := emails.AddSuppression(ctx, store.New(pool), projectID, strings.ToUpper(input.To), "unsubscribe"); err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{send: captured}
	d := dispatcher(pool, p, nil)
	if err := d.SendEmail(ctx, args(e), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, e.ID); p.calls != 0 || s.status != "failed" || s.code() != emails.CodeSuppressed || s.hasContent {
		t.Fatalf("calls=%d state=%+v (code %q), want failed/suppressed without sending", p.calls, s, s.code())
	}
	// The same address in another project is not suppressed there.
	if err := d.SendEmail(ctx, args(eOther), attempt(1)); err != nil {
		t.Fatal(err)
	}
	if s := state(t, pool, eOther.ID); p.calls != 1 || s.status != "captured" {
		t.Fatalf("other project: calls=%d state=%+v, want captured", p.calls, s)
	}
}
