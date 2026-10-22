package templates

import (
	"container/list"
	"sync"

	"github.com/google/uuid"
)

// cacheKey names one published version. Versions are immutable, so a
// compiled version never goes stale; the project is part of the key so a
// cached entry is only ever found through that project's own lookup.
type cacheKey struct {
	project, template uuid.UUID
	version           int
}

// Cache keeps recently used compiled versions, bounded by the bytes of
// their source: a compiled version holds roughly 60 times its source size
// in parse trees (measured: 1 MiB of dense actions ≈ 59 MiB of heap), so
// large versions are not cached at all. It is safe for concurrent use.
type Cache struct {
	mu            sync.Mutex
	maxBytes      int
	maxEntryBytes int
	bytes         int
	order         *list.List // front = most recent; values are *cacheEntry
	items         map[cacheKey]*list.Element
}

type cacheEntry struct {
	key  cacheKey
	c    *Compiled
	size int
}

// NewCache keeps at most maxBytes of source and skips versions over
// maxEntryBytes.
func NewCache(maxBytes, maxEntryBytes int) *Cache {
	return &Cache{maxBytes: maxBytes, maxEntryBytes: maxEntryBytes, order: list.New(), items: map[cacheKey]*list.Element{}}
}

func (c *Cache) get(k cacheKey) *Compiled {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[k]
	if !ok {
		return nil
	}
	c.order.MoveToFront(e)
	return e.Value.(*cacheEntry).c
}

// put caches a compiled version whose source is size bytes.
func (c *Cache) put(k cacheKey, compiled *Compiled, size int) {
	if size > c.maxEntryBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return
	}
	c.items[k] = c.order.PushFront(&cacheEntry{key: k, c: compiled, size: size})
	c.bytes += size
	for c.bytes > c.maxBytes {
		last := c.order.Back()
		e := last.Value.(*cacheEntry)
		c.order.Remove(last)
		delete(c.items, e.key)
		c.bytes -= e.size
	}
}

// Lookup returns a cached compiled version without loading anything (nil
// on a miss).
func (c *Cache) Lookup(projectID, templateID uuid.UUID, version int) *Compiled {
	return c.get(cacheKey{project: projectID, template: templateID, version: version})
}

// Compiled returns the compiled form of a stored version, compiling and
// caching it on a miss (compiled reports a miss).
func (c *Cache) Compiled(v Version) (_ *Compiled, compiled bool, _ error) {
	k := cacheKey{project: v.ProjectID, template: v.TemplateID, version: v.Version}
	if hit := c.get(k); hit != nil {
		return hit, false, nil
	}
	comp, err := v.Compile()
	if err != nil {
		return nil, true, err
	}
	c.put(k, comp, len(v.Source.Subject)+len(v.Source.Text)+len(v.Source.HTML))
	return comp, true, nil
}
