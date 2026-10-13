package jobs

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestRetryDelayBounds(t *testing.T) {
	t.Parallel()
	p := RetryPolicy{Base: 30 * time.Second, Max: time.Hour}
	rnd := rand.New(rand.NewPCG(1, 2)).Float64
	for n := 1; n <= 200; n++ {
		d := min(p.Max, p.Base<<min(n-1, 20))
		seen := map[time.Duration]bool{}
		for range 200 {
			got := p.Delay(n, rnd)
			if got < d/2 || got > d {
				t.Fatalf("retry %d: delay %v outside [%v, %v]", n, got, d/2, d)
			}
			seen[got] = true
		}
		if len(seen) < 50 {
			t.Fatalf("retry %d: only %d distinct delays in 200 samples; jitter missing", n, len(seen))
		}
	}
}

func TestRetryDelayExtremes(t *testing.T) {
	t.Parallel()
	p := RetryPolicy{Base: time.Second, Max: 10 * time.Second}
	zero := func() float64 { return 0 }
	almostOne := func() float64 { return 0.999999 }
	for n, want := range map[int]time.Duration{1: 500 * time.Millisecond, 2: time.Second, 3: 2 * time.Second, 5: 5 * time.Second, 64: 5 * time.Second, 1 << 30: 5 * time.Second} {
		if got := p.Delay(n, zero); got != want {
			t.Errorf("Delay(%d, 0) = %v, want %v", n, got, want)
		}
	}
	if got := p.Delay(1<<30, almostOne); got > p.Max || got < p.Max*9/10 {
		t.Errorf("huge retry number: %v, want just below %v (no overflow)", got, p.Max)
	}
	if got := p.Delay(0, zero); got != 500*time.Millisecond {
		t.Errorf("Delay(0) = %v, want treated as first retry", got)
	}
}

func TestRetryWorstCase(t *testing.T) {
	t.Parallel()
	p := RetryPolicy{Base: 30 * time.Second, Max: time.Hour}
	// 7 retries: 30s+1m+2m+4m+8m+16m+32m = 3810s.
	if got := p.WorstCase(8); got != 3810*time.Second {
		t.Errorf("WorstCase(8) = %v, want 3810s", got)
	}
	if got := p.WorstCase(1); got != 0 {
		t.Errorf("WorstCase(1) = %v, want 0 (no retry)", got)
	}
}
