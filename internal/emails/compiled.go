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

// compiledCache keeps recently used compiled template versions, bounded by
// the bytes of their source: a compiled version holds roughly 60 times its
// source size in parse trees (measured: 1 MiB of dense actions ≈ 59 MiB of
// heap), so large versions are not cached at all.
type compiledCache struct {
	mu            sync.Mutex
	maxBytes      int
	maxEntryBytes int
	bytes         int
	order         *list.List // front = most recent; values are *compiledEntry
	items         map[compiledKey]*list.Element
}

type compiledEntry struct {
	key  compiledKey
	c    *templates.Compiled
	size int
}

func newCompiledCache(maxBytes, maxEntryBytes int) *compiledCache {
	return &compiledCache{maxBytes: maxBytes, maxEntryBytes: maxEntryBytes, order: list.New(), items: map[compiledKey]*list.Element{}}
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

// put caches a compiled version whose source is size bytes.
func (c *compiledCache) put(k compiledKey, compiled *templates.Compiled, size int) {
	if size > c.maxEntryBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return
	}
	c.items[k] = c.order.PushFront(&compiledEntry{key: k, c: compiled, size: size})
	c.bytes += size
	for c.bytes > c.maxBytes {
		last := c.order.Back()
		e := last.Value.(*compiledEntry)
		c.order.Remove(last)
		delete(c.items, e.key)
		c.bytes -= e.size
	}
}
