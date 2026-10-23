//go:build integration

package metrics_test

import (
	"context"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/skraus2/email-platform/internal/metrics"
	"github.com/skraus2/email-platform/internal/testdb"
)

func TestPoolCollector(t *testing.T) {
	t.Parallel()
	pool := testdb.NewMigrated(t)
	reg := prometheus.NewRegistry()
	metrics.RegisterPool(reg, pool)
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	got := series(t, reg, "email_platform_db_pool_")
	// The first acquire waited for a new connection (the pool starts empty),
	// so the wait counter is positive.
	for _, want := range []string{"email_platform_db_pool_acquired_conns{} 1", "email_platform_db_pool_total_conns{} 1"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if slices.Contains(got, "email_platform_db_pool_max_conns{} 0") {
		t.Errorf("max_conns is 0: %q", got)
	}
	if len(got) != 5 {
		t.Errorf("pool series %q, want 5", got)
	}
}
