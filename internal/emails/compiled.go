package emails

import (
	"container/list"
	"sync"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/templates"
)

// compiledKey names one published version. Versions are immutable, so a
// compiled version never goes stale; the project is part of the key so a
// cached entry is only ever found through that project's own lookup.
type compiledKey struct {
	project, template uuid.UUID
	version           int
}

// compiledCache keeps the most recently used compiled template versions.
type compiledCache struct {
	mu    sync.Mutex
	max   int
	order *list.List // front = most recent; values are *compiledEntry
	items map[compiledKey]*list.Element
}

type compiledEntry struct {
	key compiledKey
	c   *templates.Compiled
}

func newCompiledCache(maxEntries int) *compiledCache {
	return &compiledCache{max: maxEntries, order: list.New(), items: map[compiledKey]*list.Element{}}
}

func (c *compiledCache) get(k compiledKey) *templates.Compiled {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[k]
	if !ok {
		return nil
	}
	c.order.MoveToFront(e)
	return e.Value.(*compiledEntry).c
}

func (c *compiledCache) put(k compiledKey, compiled *templates.Compiled) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return
	}
	c.items[k] = c.order.PushFront(&compiledEntry{key: k, c: compiled})
	for c.order.Len() > c.max {
		last := c.order.Back()
		c.order.Remove(last)
		delete(c.items, last.Value.(*compiledEntry).key)
	}
}
