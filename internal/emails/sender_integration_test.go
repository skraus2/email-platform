//go:build integration

package emails_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/emails"
	"github.com/skraus2/email-platform/internal/jobs"
	"github.com/skraus2/email-platform/internal/store"
)

func TestSenderAdmin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	q := store.New(pool)

	addr, err := emails.AddSender(ctx, q, projectID, emails.SenderAddress, "NoReply@Example.test")
	if err != nil {
		t.Fatal(err)
	}
	if addr.Value != "noreply@example.test" || addr.Kind != "address" || addr.Status != "pending" || addr.VerifiedAt != nil {
		t.Fatalf("added = %+v", addr)
	}
	dom, err := emails.AddSender(ctx, q, projectID, emails.SenderDomain, "Mail.Example.test")
	if err != nil || dom.Value != "mail.example.test" || dom.Kind != "domain" {
		t.Fatalf("domain = %+v, %v", dom, err)
	}
	if _, err := emails.AddSender(ctx, q, projectID, emails.SenderAddress, "noreply@EXAMPLE.test"); !errors.Is(err, emails.ErrSenderExists) {
		t.Fatalf("duplicate: err = %v, want ErrSenderExists", err)
	}

	for name, tc := range map[string]struct {
		kind  emails.SenderKind
		value string
	}{
		"address with display name": {emails.SenderAddress, "Name <a@example.test>"},
		"address without at":        {emails.SenderAddress, "example.test"},
		"address unicode":           {emails.SenderAddress, "a@bücher.test"},
		"domain with at":            {emails.SenderDomain, "a@example.test"},
		"domain single label":       {emails.SenderDomain, "localhost"},
		"domain bad char":           {emails.SenderDomain, "exa_mple.test"},
		"domain leading hyphen":     {emails.SenderDomain, "-example.test"},
		"domain empty label":        {emails.SenderDomain, "example..test"},
		"domain trailing dot":       {emails.SenderDomain, "example.test."},
		"domain unicode":            {emails.SenderDomain, "bücher.test"},
		"unknown kind":              {"wildcard", "example.test"},
	} {
		if _, err := emails.AddSender(ctx, q, projectID, tc.kind, tc.value); err == nil || errors.Is(err, emails.ErrSenderExists) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}

	ok, err := emails.VerifySender(ctx, q, addr.ID)
	if err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	if ok, err := emails.VerifySender(ctx, q, addr.ID); err != nil || ok {
		t.Fatalf("second verify = %v, %v; want already verified", ok, err)
	}
	list, err := emails.ListSenders(ctx, q, projectID, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[1].Value != "noreply@example.test" || list[1].Status != "verified" || list[1].VerifiedAt == nil {
		t.Fatalf("verified identity listed as %+v", list[1])
	}

	// The same identity may exist in another project independently.
	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.AddSender(ctx, q, other.ID, emails.SenderAddress, "noreply@example.test"); err != nil {
		t.Fatalf("other project: %v", err)
	}

	removed, err := emails.RemoveSender(ctx, q, dom.ID)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if removed, err := emails.RemoveSender(ctx, q, dom.ID); err != nil || removed {
		t.Fatalf("second remove = %v, %v", removed, err)
	}
	if list, err := emails.ListSenders(ctx, q, other.ID, 0); err != nil || len(list) != 1 {
		t.Fatalf("other project's identities = %+v, %v", list, err)
	}
}

func verifiedSender(t *testing.T, q *store.Queries, projectID uuid.UUID, kind emails.SenderKind, value string) emails.SenderIdentity {
	t.Helper()
	ctx := context.Background()
	id, err := emails.AddSender(ctx, q, projectID, kind, value)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := emails.VerifySender(ctx, q, id.ID); err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	return id
}

func TestVerifiedSenderRequired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	q := store.New(pool)
	svc := newService(t, pool, nil)
	svc.RequireVerifiedSenders()

	other, err := auth.CreateProject(ctx, q, "other")
	if err != nil {
		t.Fatal(err)
	}
	verifiedSender(t, q, other.ID, emails.SenderDomain, "example.test")
	verifiedSender(t, q, projectID, emails.SenderAddress, "billing@corp.test")
	verifiedSender(t, q, projectID, emails.SenderDomain, "mail.example.test")
	if _, err := emails.AddSender(ctx, q, projectID, emails.SenderDomain, "pending.test"); err != nil {
		t.Fatal(err)
	}

	from := func(addr string) emails.CreateInput {
		in := input
		in.From = addr
		return in
	}
	for _, refused := range []string{
		"noreply@example.test",          // domain verified only in another project
		"other@corp.test",               // only billing@corp.test is verified
		"noreply@sub.mail.example.test", // no subdomain match
		"noreply@pending.test",          // pending identity
		"noreply@example.test.evil",     // suffix tricks
	} {
		if _, err := svc.Create(ctx, projectID, from(refused)); !errors.Is(err, emails.ErrSenderNotVerified) {
			t.Errorf("from %s: err = %v, want ErrSenderNotVerified", refused, err)
		}
	}
	if got := counts(t, pool); got != (rowCounts{}) {
		t.Fatalf("rows = %+v, want nothing stored", got)
	}
	for _, accepted := range []string{"billing@corp.test", "Billing@CORP.test", "noreply@mail.example.test", "x@MAIL.example.TEST"} {
		if _, err := svc.Create(ctx, projectID, from(accepted)); err != nil {
			t.Errorf("from %s: %v", accepted, err)
		}
	}
}

// A refusal for an unverified sender consumes no rate-limit budget, and the
// sender check runs before suppression.
func TestUnverifiedSenderRefusalOrderAndBudget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	q := store.New(pool)
	enq, err := jobs.NewEnqueuer(pool, slog.New(slog.DiscardHandler), testMaxAttempts)
	if err != nil {
		t.Fatal(err)
	}
	svc := emails.NewService(pool, enq, slog.New(slog.DiscardHandler), 1)
	svc.RequireVerifiedSenders()
	if _, err := emails.AddSuppression(ctx, q, projectID, input.To, "manual"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := svc.Create(ctx, projectID, input); !errors.Is(err, emails.ErrSenderNotVerified) {
			t.Fatalf("err = %v, want ErrSenderNotVerified (checked before suppression)", err)
		}
	}
	verifiedSender(t, q, projectID, emails.SenderDomain, "example.test")
	if _, err := emails.RemoveSuppression(ctx, q, projectID, input.To); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, projectID, input); err != nil {
		t.Fatalf("first accepted send within a limit of 1: %v (refusals must not count)", err)
	}
}

// Without the requirement (local test sink default), senders are not checked.
func TestSenderCheckOffByDefault(t *testing.T) {
	t.Parallel()
	pool, projectID := setup(t)
	if _, err := newService(t, pool, nil).Create(context.Background(), projectID, input); err != nil {
		t.Fatal(err)
	}
}

// A replay of an email accepted before its sender identity was removed
// still returns that email.
func TestReplayAfterSenderRemoved(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, projectID := setup(t)
	q := store.New(pool)
	svc := newService(t, pool, nil)
	svc.RequireVerifiedSenders()
	id := verifiedSender(t, q, projectID, emails.SenderDomain, "example.test")
	first, err := svc.Create(ctx, projectID, keyed("k1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emails.RemoveSender(ctx, q, id.ID); err != nil {
		t.Fatal(err)
	}
	again, err := svc.Create(ctx, projectID, keyed("k1"))
	if err != nil || !again.Replayed || again.ID != first.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := svc.Create(ctx, projectID, keyed("k2")); !errors.Is(err, emails.ErrSenderNotVerified) {
		t.Fatalf("new key: err = %v, want ErrSenderNotVerified", err)
	}
}
