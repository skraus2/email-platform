package emails

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/skraus2/email-platform/internal/metrics"
)

func TestReconcileWait(t *testing.T) {
	c := ReconcileConfig{Window: 5 * time.Minute, MaxWait: time.Hour, HealthyWithin: 2 * time.Minute, QueueKey: "k"}
	boom := errors.New("db down")
	for _, tc := range []struct {
		name    string
		age     time.Duration
		healthy bool
		err     error
		wait    time.Duration
		result  string
		asked   bool
	}{
		{name: "just after the attempt", age: 0, wait: 5 * time.Minute, result: ReconcileWaiting},
		{name: "within the window", age: 4 * time.Minute, healthy: true, wait: time.Minute, result: ReconcileWaiting},
		{name: "window over, queue healthy", age: 5 * time.Minute, healthy: true, result: ReconcileResent, asked: true},
		{name: "window over, queue unhealthy", age: 6 * time.Minute, wait: unhealthyRecheck, result: ReconcilePipelineUnhealthy, asked: true},
		{name: "near the max wait", age: 58 * time.Minute, wait: 2 * time.Minute, result: ReconcilePipelineUnhealthy, asked: true},
		{name: "max wait reached", age: time.Hour, result: ReconcileResent},
		{name: "health unknown", age: 10 * time.Minute, err: boom, asked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			wait, result, err := reconcileWait(c, tc.age, func() (bool, error) { asked = true; return tc.healthy, tc.err })
			if !errors.Is(err, tc.err) || wait != tc.wait || result != tc.result || asked != tc.asked {
				t.Fatalf("got %v %q %v (asked %v), want %v %q %v (asked %v)", wait, result, err, asked, tc.wait, tc.result, tc.err, tc.asked)
			}
		})
	}
}

func TestReconcileConfigValidated(t *testing.T) {
	ok := ReconcileConfig{Window: 5 * time.Minute, MaxWait: time.Hour, HealthyWithin: 2 * time.Minute, QueueKey: "k"}
	for name, c := range map[string]ReconcileConfig{
		"short window": {Window: time.Second, MaxWait: time.Hour, HealthyWithin: 2 * time.Minute, QueueKey: "k"},
		"max below":    {Window: 5 * time.Minute, MaxWait: time.Minute, HealthyWithin: 2 * time.Minute, QueueKey: "k"},
		"short health": {Window: 5 * time.Minute, MaxWait: time.Hour, HealthyWithin: time.Second, QueueKey: "k"},
		"no queue":     {Window: 5 * time.Minute, MaxWait: time.Hour, HealthyWithin: 2 * time.Minute},
	} {
		if err := (&Dispatcher{}).Reconcile(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (&Dispatcher{}).Reconcile(ok); err != nil {
		t.Fatal(err)
	}
}

// The labels exported at zero are the results the dispatcher counts.
func TestReconcileResultsMatchMetricLabels(t *testing.T) {
	want := []string{ReconcileFound, ReconcileWaiting, ReconcilePipelineUnhealthy, ReconcileResent}
	if !slices.Equal(metrics.ReconcileResults, want) {
		t.Fatalf("metric labels %v, results %v", metrics.ReconcileResults, want)
	}
}
