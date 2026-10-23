//go:build integration

package webhooks

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/metrics"
)

func deliveryResults(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range families {
		for _, m := range f.GetMetric() {
			out[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}
	return out
}

func TestDeliverCountsResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newDeliveryFixture(t)
	reg := prometheus.NewRegistry()
	m := metrics.NewWebhooks(reg)
	if got := deliveryResults(t, reg); len(got) != 3 || got["delivered"] != 0 || got["retry"] != 0 || got["failed"] != 0 {
		t.Fatalf("initial series %v", got)
	}

	ok := newReceiver(t, http.StatusOK)
	f.endpoint(t, ok.srv.URL+"/hook")
	_, deliveries := f.change(t, "captured")
	d := f.deliverer(t, lax, 2, 5*time.Second)
	d.Observe(m)
	if err := d.Deliver(ctx, deliveries[0], attempt(1)); err != nil {
		t.Fatal(err)
	}
	// A repeated job for the delivered delivery counts nothing.
	if err := d.Deliver(ctx, deliveries[0], attempt(2)); err != nil {
		t.Fatal(err)
	}

	g := newDeliveryFixture(t)
	down := newReceiver(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	g.endpoint(t, down.srv.URL+"/hook")
	_, failing := g.change(t, "failed")
	d2 := g.deliverer(t, lax, 2, 5*time.Second)
	d2.Observe(m)
	_ = d2.Deliver(ctx, failing[0], attempt(1))
	if err := d2.Deliver(ctx, failing[0], attempt(2)); err != nil {
		t.Fatal(err)
	}
	if got := deliveryResults(t, reg); got["delivered"] != 1 || got["retry"] != 1 || got["failed"] != 1 {
		t.Errorf("results %v, want delivered 1, retry 1, failed 1", got)
	}

	// A 4xx answer fails the delivery without a retry.
	h := newDeliveryFixture(t)
	gone := newReceiver(t, http.StatusGone)
	h.endpoint(t, gone.srv.URL+"/hook")
	_, refused := h.change(t, "captured")
	d3 := h.deliverer(t, lax, 5, 5*time.Second)
	d3.Observe(m)
	if err := d3.Deliver(ctx, refused[0], attempt(1)); err != nil {
		t.Fatal(err)
	}
	// A delivery stuck pending for 30 days is expired by the safety net.
	_, stale := h.change(t, "failed")
	if _, err := h.pool.Exec(ctx, "UPDATE webhook_deliveries SET created_at = now() - interval '30 days', updated_at = now() - interval '30 days' WHERE id = $1", stale[0].DeliveryID); err != nil {
		t.Fatal(err)
	}
	if err := d3.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if got := deliveryResults(t, reg); got["failed"] != 3 || got["retry"] != 1 {
		t.Errorf("after a 4xx and an expiry: %v, want failed 3, retry 1", got)
	}
}
