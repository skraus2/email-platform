package admin

import (
	"container/list"
	"sync"
	"time"
)

// Lockout policy: from the fifth consecutive failure a name is locked for
// 1, 2, 4 … minutes, at most 15; failures during a lock extend it.
const (
	lockAfter   = 5
	maxLockTime = 15 * time.Minute
)

// lockDuration is the lock after failures consecutive failures (0 below the
// threshold).
func lockDuration(failures int) time.Duration {
	if failures < lockAfter {
		return 0
	}
	d := time.Minute << min(failures-lockAfter, 10)
	return min(d, maxLockTime)
}

// throttle applies the lockout policy to usernames without an account, in
// memory and bounded (least recently failed names are dropped), so probing
// unknown names is slowed like probing real ones. It resets on restart.
type throttle struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[string]*list.Element
}

type throttled struct {
	name        string
	failures    int
	lockedUntil time.Time
}

func newThrottle(maxNames int) *throttle {
	return &throttle{max: maxNames, order: list.New(), items: map[string]*list.Element{}}
}

// locked returns how long name stays locked at now (0 = not locked).
func (t *throttle) locked(name string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.items[name]; ok {
		if d := e.Value.(*throttled).lockedUntil.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// fail records one failure for name at now.
func (t *throttle) fail(name string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.items[name]
	if !ok {
		e = t.order.PushFront(&throttled{name: name})
		t.items[name] = e
		for t.order.Len() > t.max {
			last := t.order.Back()
			t.order.Remove(last)
			delete(t.items, last.Value.(*throttled).name)
		}
	}
	t.order.MoveToFront(e)
	v := e.Value.(*throttled)
	v.failures++
	if d := lockDuration(v.failures); d > 0 {
		v.lockedUntil = now.Add(d)
	}
}
