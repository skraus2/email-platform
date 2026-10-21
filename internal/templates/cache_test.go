package templates

import (
	"testing"

	"github.com/google/uuid"
)

func TestCacheIsBoundedByBytes(t *testing.T) {
	c := NewCache(100, 40)
	key := func(v int) cacheKey { return cacheKey{project: uuid.Nil, template: uuid.Nil, version: v} }
	c.put(key(1), nil, 41) // too large to cache
	if _, ok := c.items[key(1)]; ok {
		t.Fatal("oversized entry cached")
	}
	for v := 2; v <= 5; v++ {
		c.put(key(v), nil, 30)
	}
	if c.bytes > 100 || len(c.items) != 3 {
		t.Fatalf("bytes %d entries %d, want ≤ 100 and 3", c.bytes, len(c.items))
	}
	if _, ok := c.items[key(2)]; ok {
		t.Fatal("least recently used entry kept")
	}
	c.get(key(3)) // now most recent
	c.put(key(6), nil, 30)
	if _, ok := c.items[key(3)]; !ok {
		t.Fatal("recently used entry evicted")
	}
	if _, ok := c.items[key(4)]; ok {
		t.Fatal("least recently used entry kept after a get")
	}
}
