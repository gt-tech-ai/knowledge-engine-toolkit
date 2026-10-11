// Package dedup provides message deduplication for exactly-once processing under
// at-least-once delivery. The default Memory backend is single-process; a shared store
// (Redis/Postgres) is the cross-replica sibling that lands when a second replica does.
package dedup

import (
	"context"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Memory is an in-memory Deduplicator with an optional TTL. A zero TTL means keys never
// expire (bounded only by memory); a positive TTL lets a key be re-processed after it
// lapses.
type Memory struct {
	// seen maps a processed key to the time it was first recorded.
	seen map[string]time.Time
	// ttl is the expiry after which a key may be re-processed (0 = never expires).
	ttl time.Duration
	// mu guards seen.
	mu sync.Mutex
}

// NewMemory builds an in-memory Deduplicator. ttl of zero remembers keys forever.
func NewMemory(ttl time.Duration) *Memory {
	return &Memory{seen: make(map[string]time.Time), ttl: ttl}
}

// compile-time check: Memory satisfies the Deduplicator contract.
var _ interfaces.Deduplicator = (*Memory)(nil)

// Seen reports whether key was already processed (a duplicate to skip) and, if not,
// records it as processed. A key older than the TTL is treated as first-seen again.
func (m *Memory) Seen(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if seenAt, ok := m.seen[key]; ok && (m.ttl == 0 || now.Sub(seenAt) < m.ttl) {
		return true, nil
	}
	m.seen[key] = now
	return false, nil
}
