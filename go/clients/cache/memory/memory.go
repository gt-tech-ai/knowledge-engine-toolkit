// Package memory is the in-process ByteCache: a bounded, least-recently-used map whose
// entries expire at their TTL. It suits a value a service reads often, that rarely
// changes, and that each replica may hold its own copy of, when no shared cache is
// deployed; replicas never see each other's writes or deletes.
package memory

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// DefaultMaxEntries bounds the cache when the configured bound is not positive.
const DefaultMaxEntries = 10_000

// entry is one cached value and when it expires.
type entry struct {
	// expires is when the value stops being served.
	expires time.Time

	// key is the entry's key, so an evicted list element finds its map slot.
	key string

	// value is a private copy of the bytes Set stored.
	value []byte
}

// Cache is the in-process ByteCache. It is safe for concurrent use.
type Cache struct {
	// now reads the clock every expiry is measured against.
	now func() time.Time

	// items maps a key to its element in order.
	items map[string]*list.Element

	// order holds the entries, most recently used first.
	order *list.List

	// mu guards items and order.
	mu sync.Mutex

	// max bounds how many entries the cache holds.
	max int

	// ttl applies when a caller passes a zero TTL.
	ttl time.Duration
}

var _ interfaces.ByteCache = (*Cache)(nil)

// New returns a cache holding at most maxEntries values (DefaultMaxEntries when not
// positive), applying defaultTTL to a zero-TTL Set and measuring expiry with now
// (time.Now in production).
func New(maxEntries int, defaultTTL time.Duration, now func() time.Time) *Cache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}

	return &Cache{
		now:   now,
		items: map[string]*list.Element{},
		order: list.New(),
		max:   maxEntries,
		ttl:   defaultTTL,
		mu:    sync.Mutex{},
	}
}

// Get returns a copy of key's value and marks it recently used; an expired value is
// removed and misses.
func (c *Cache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.items[key]
	if !ok {
		return nil, false
	}

	currentEntry := stdx.As[*entry](element.Value)
	if !c.now().Before(currentEntry.expires) {
		c.remove(element)

		return nil, false
	}

	c.order.MoveToFront(element)

	return append([]byte(nil), currentEntry.value...), true
}

// Set stores a copy of value under key for ttl (the default TTL when zero), evicting
// the least recently used entry when the cache is full. A non-positive effective TTL
// stores nothing.
func (c *Cache) Set(_ context.Context, key string, value []byte, ttl time.Duration) {
	if ttl == 0 {
		ttl = c.ttl
	}

	if ttl <= 0 {
		return
	}

	currentEntry := &entry{
		key:     key,
		value:   append([]byte(nil), value...),
		expires: c.now().Add(ttl),
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		el.Value = currentEntry
		c.order.MoveToFront(el)

		return
	}

	c.items[key] = c.order.PushFront(currentEntry)
	for c.order.Len() > c.max {
		c.remove(c.order.Back())
	}
}

// Delete removes key's value.
func (c *Cache) Delete(_ context.Context, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
}

// remove drops el from both the order and the map; the caller holds mu.
func (c *Cache) remove(el *list.Element) {
	e := stdx.As[*entry](c.order.Remove(el))
	delete(c.items, e.key)
}
