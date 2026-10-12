package interfaces

// KeyedLimiter caps how much work runs at once per key — a tenant, a user, a source — so
// one key's load cannot take every slot from the others. It never blocks: a key at its
// cap is refused, and the caller decides how to answer (a 429, a terminal frame, a
// requeue).
//
// Implementations: bulkhead/keyed (in-process).
type KeyedLimiter interface {
	// TryAcquire takes one of key's slots. ok is false when key is at its cap; otherwise
	// release frees the slot and must be called exactly once.
	TryAcquire(key string) (release func(), ok bool)
}
