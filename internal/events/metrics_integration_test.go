//go:build integration

package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/events"
	"github.com/skraus2/email-platform/internal/metrics"
)

func counterValue(t *testing.T, reg *prometheus.Registry, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "email_platform_provider_events_total" {
			continue
		}
	next:
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if labels[l.GetName()] != l.GetValue() {
					continue next
				}
			}
			return m.GetCounter().GetValue()
		}
	}
	t.Fatalf("no series %v", labels)
	return 0
}

func TestIngestCountsEventsAfterCommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	reg := prometheus.NewRegistry()
	f.rec.Observe(metrics.NewProviderEvents(reg, events.ProviderEventTypes(), events.IngestOutcomes()))
	id := f.email(t, "submitted", "ses")
	key := func(typ, bounce, outcome string) map[string]string {
		return map[string]string{"type": typ, "bounce_type": bounce, "outcome": outcome}
	}
	if v := counterValue(t, reg, key("bounced", "transient", "stored")); v != 0 {
		t.Fatalf("starts at %v", v)
	}

	f.ingest(t, delivery(f, id))
	f.ingest(t, delivery(f, id)) // redelivered by SQS
	f.ingest(t, permanent(f, id))
	f.ingest(t, f.ev(id, "bounced", "fb-transient", map[string]string{"bounce_type": "Transient"}))
	f.ingest(t, f.ev(uuid.New(), "complained", "fb-unknown", nil))
	if _, err := f.rec.Ingest(context.Background(), f.ev(id, "opened", "x", nil)); !errors.Is(err, events.ErrInvalidEvent) {
		t.Fatalf("invalid event: %v", err)
	}

	for _, c := range []struct {
		labels map[string]string
		want   float64
	}{
		{key("delivered", "none", "stored"), 1},
		{key("delivered", "none", "duplicate"), 1},
		{key("bounced", "permanent", "stored"), 1},
		{key("bounced", "transient", "stored"), 1},
		{key("complained", "none", "unknown_email"), 1},
		{key("complained", "none", "stored"), 0},
	} {
		if got := counterValue(t, reg, c.labels); got != c.want {
			t.Errorf("%v = %v, want %v", c.labels, got, c.want)
		}
	}
	// Only the known combinations exist: the invalid event added none.
	families, _ := reg.Gather()
	if n := len(families[0].GetMetric()); n != 5*5+3*5 {
		t.Errorf("%d series, want the 40 pre-registered ones", n)
	}
}
