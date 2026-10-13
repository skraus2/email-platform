package emails

import (
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestBucketBurstThenRate(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	b := newBucket(10, clk.Now) // 10/s, burst 10
	for i := range 10 {
		if wait, ok := b.TryTake(0); !ok || wait != 0 {
			t.Fatalf("burst token %d: wait %v ok %v", i, wait, ok)
		}
	}
	// Empty: the next token is 100 ms away.
	wait, ok := b.TryTake(0)
	if ok || wait != 100*time.Millisecond {
		t.Fatalf("empty bucket: wait %v ok %v, want 100ms not taken", wait, ok)
	}
	// Refusing consumed nothing: asking again right away gives the same wait.
	if wait2, ok := b.TryTake(0); ok || wait2 != wait {
		t.Fatalf("refusal consumed capacity: %v %v", wait2, ok)
	}
	// Within maxWait the token is taken and the caller told to wait.
	if wait, ok := b.TryTake(time.Second); !ok || wait != 100*time.Millisecond {
		t.Fatalf("take with wait: %v %v", wait, ok)
	}
	// That reservation moved the next free slot to 200 ms.
	if wait, ok := b.TryTake(150 * time.Millisecond); ok || wait != 200*time.Millisecond {
		t.Fatalf("after reservation: %v %v, want 200ms not taken", wait, ok)
	}
	clk.now = clk.now.Add(10 * time.Second)
	// Refill is capped at the burst.
	for i := range 10 {
		if _, ok := b.TryTake(0); !ok {
			t.Fatalf("refilled token %d missing", i)
		}
	}
	if _, ok := b.TryTake(0); ok {
		t.Fatal("bucket refilled beyond its burst")
	}
}

func TestBucketNeverExceedsRate(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	const rate = 5
	b := newBucket(rate, clk.Now)
	sent := map[int64]int{} // per second
	for step := range 10_000 {
		clk.now = time.Unix(0, 0).Add(time.Duration(step) * time.Millisecond)
		if wait, ok := b.TryTake(0); ok && wait == 0 {
			sent[clk.now.Unix()]++
		}
	}
	for sec, n := range sent {
		limit := rate
		if sec == 0 {
			limit = 2 * rate // burst + refill in the first second
		}
		if n > limit {
			t.Errorf("second %d: %d sends, want ≤ %d", sec, n, limit)
		}
	}
	if total := len(sent); total < 9 {
		t.Errorf("only %d seconds with sends; bucket stalls", total)
	}
}

func TestBucketSubOneRate(t *testing.T) {
	t.Parallel()
	clk := &fakeClock{now: time.Unix(0, 0)}
	b := newBucket(0.5, clk.Now) // one send every 2 s, burst 1
	if _, ok := b.TryTake(0); !ok {
		t.Fatal("first token missing")
	}
	if wait, ok := b.TryTake(0); ok || wait != 2*time.Second {
		t.Fatalf("wait = %v ok %v, want 2s", wait, ok)
	}
}
