package emails

import (
	"math"
	"sync"
	"time"
)

// bucket limits how fast the worker hands messages to the provider: rate
// per second with a burst of max(1, rate). It is a token bucket expressed as
// the time the next token becomes free (GCRA), so a refused request leaves
// no trace and cannot push later sends out.
type bucket struct {
	mu       sync.Mutex
	now      func() time.Time
	interval time.Duration // time per token
	slack    time.Duration // how far ahead tokens may be taken (burst-1 tokens)
	next     time.Time     // when the bucket would be empty at full use
}

func newBucket(perSecond float64, now func() time.Time) *bucket {
	burst := max(1, math.Floor(perSecond))
	interval := time.Duration(float64(time.Second) / perSecond)
	return &bucket{now: now, interval: interval, slack: time.Duration(burst-1) * interval}
}

// TryTake takes a token if one is free within maxWait and returns how long
// the caller must wait before using it. Otherwise it takes nothing and
// returns how long until a token would be free.
func (b *bucket) TryTake(maxWait time.Duration) (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	start := b.next
	if start.Before(now) {
		start = now
	}
	wait := max(start.Sub(now)-b.slack, 0)
	if wait > maxWait {
		return wait, false
	}
	b.next = start.Add(b.interval)
	return wait, true
}
