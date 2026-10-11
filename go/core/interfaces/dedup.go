package interfaces

import "context"

// Deduplicator provides idempotency for at-least-once delivery: it records the
// keys it has seen so a redelivered message is processed exactly once.
//
// Implementations: an in-memory TTL store (default), or a shared store (Redis/Postgres)
// for cross-replica dedup. Handler code depends on this interface, never a concrete
// store.
type Deduplicator interface {
	// Seen atomically reports whether key was already processed and, if not, marks it
	// processed. It returns true when key is a duplicate (the caller should skip it) and
	// false when this is the first sighting (the caller should process it).
	Seen(ctx context.Context, key string) (bool, error)
}
