//go:build integration

package events_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/metrics"
	"github.com/skraus2/email-platform/internal/store"
)

// observeDuplicates makes f's recorder count duplicate sends and returns a
// reader for the confirmed count.
func observeDuplicates(t *testing.T, f *fixture) func() float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	f.rec.ObserveDuplicates(metrics.NewDuplicates(reg))
	return func() float64 {
		t.Helper()
		families, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, fam := range families {
			for _, m := range fam.GetMetric() {
				for _, l := range m.GetLabel() {
					if l.GetName() == "kind" && l.GetValue() == "confirmed" {
						return m.GetCounter().GetValue()
					}
				}
			}
		}
		t.Fatal("no confirmed duplicate counter")
		return 0
	}
}

// assertConfirmedLog checks the duplicate warning names the email and its
// project, never the recipient.
func assertConfirmedLog(t *testing.T, f *fixture, id uuid.UUID) {
	t.Helper()
	for line := range strings.SplitSeq(f.logs.String(), "\n") {
		if !strings.Contains(line, "confirmed_duplicate_send") {
			continue
		}
		if !strings.Contains(line, id.String()) || !strings.Contains(line, f.projectID.String()) ||
			strings.Contains(strings.ToLower(line), strings.ToLower(recipient)) {
			t.Fatalf("warning %s", line)
		}
		return
	}
	t.Fatalf("no confirmed_duplicate_send warning:\n%s", f.logs)
}

// sent is a provider event of message msgID.
func (f *fixture) sent(id uuid.UUID, typ, msgID string) events.ProviderEvent {
	e := f.ev(id, typ, typ+":"+msgID, nil)
	e.ProviderMessageID = msgID
	return e
}

// dispatch mimics the worker's finish with providerID and counts the
// duplicates Dispatched reports once the transaction committed.
func dispatch(t *testing.T, f *fixture, id uuid.UUID, providerID string) int {
	t.Helper()
	ctx := context.Background()
	var dups int
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).MarkDispatched(ctx, store.MarkDispatchedParams{
			ID: id, ProjectID: f.projectID, Status: "submitted", Provider: new("ses"), ProviderID: &providerID,
		})
		if err != nil || n == 0 {
			t.Fatalf("mark dispatched: %d, %v", n, err)
		}
		dups, err = f.rec.Dispatched(ctx, tx, f.projectID, id, "submitted", "ses", providerID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f.rec.ConfirmedDuplicates(ctx, f.projectID, id, dups)
	return dups
}

// Several stored message ids at dispatch: every id other than the one the
// email is recorded with is a confirmed duplicate, however many events
// each id has.
func TestDispatchedCountsOtherMessageIDs(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	confirmed := observeDuplicates(t, f)
	id := f.email(t, "sending", "")
	f.ingest(t, f.sent(id, "send", "msg-a"))
	f.ingest(t, f.sent(id, "delivered", "msg-a"))
	f.ingest(t, f.sent(id, "send", "msg-b"))
	if got := dispatch(t, f, id, "msg-a"); got != 1 {
		t.Fatalf("duplicates = %d, want 1 (msg-b)", got)
	}
	if got := confirmed(); got != 1 {
		t.Fatalf("confirmed = %v, want 1", got)
	}
	assertConfirmedLog(t, f, id)
}

// AC9: a resend after events of the earlier send were stored counts the
// earlier id once at dispatch; later events of that id are not counted
// again, a new id is counted at its first event only.
func TestDuplicateCountedOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	confirmed := observeDuplicates(t, f)
	id := f.email(t, "sending", "")
	f.ingest(t, f.sent(id, "send", "msg-old"))
	if got := dispatch(t, f, id, "msg-new"); got != 1 {
		t.Fatalf("duplicates at dispatch = %d, want 1", got)
	}
	f.ingest(t, f.sent(id, "delivered", "msg-old")) // seen before: not again
	f.ingest(t, f.sent(id, "send", "msg-new"))      // the recorded id
	f.ingest(t, f.sent(id, "delivered", "msg-new"))
	if got := confirmed(); got != 1 {
		t.Fatalf("confirmed = %v, want 1", got)
	}
	f.ingest(t, f.sent(id, "send", "msg-third")) // a further copy
	f.ingest(t, f.sent(id, "delivered", "msg-third"))
	if out := f.ingest(t, f.sent(id, "delivered", "msg-third")); out != events.Duplicate {
		t.Fatalf("redelivered event = %v, want Duplicate", out)
	}
	if got := confirmed(); got != 2 {
		t.Fatalf("confirmed = %v, want 2", got)
	}
	if !strings.Contains(f.logs.String(), "possible_duplicate_send") {
		t.Fatal("existing warning for a foreign message id gone")
	}
	if n := strings.Count(f.logs.String(), "confirmed_duplicate_send"); n != 2 {
		t.Fatalf("%d confirmed_duplicate_send warnings, want 2 (dispatch and the new id)", n)
	}
	assertConfirmedLog(t, f, id)
}

// No duplicate: events of the recorded id only, before and after dispatch.
func TestNoDuplicateForOneMessageID(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	confirmed := observeDuplicates(t, f)
	id := f.email(t, "sending", "")
	f.ingest(t, f.sent(id, "send", "msg-1"))
	if got := dispatch(t, f, id, "msg-1"); got != 0 {
		t.Fatalf("duplicates = %d, want 0", got)
	}
	f.ingest(t, f.sent(id, "delivered", "msg-1"))
	if got := confirmed(); got != 0 {
		t.Fatalf("confirmed = %v, want 0", got)
	}
}
