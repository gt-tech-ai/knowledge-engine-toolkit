package interfaces

import (
	"context"
	"time"
)

// ByteCache is the non-generic cache interface for raw byte storage.
// Used by infrastructure wrappers (metrics, tracing) that operate on
// serialized values without caring about the concrete type.
//
// Compared to the generic Cache[T], ByteCache:
//   - Uses []byte values instead of typed values
//   - Returns ([]byte, bool) from Get instead of (T, bool)
//   - Set does not return an error (fire-and-forget)
//   - Has no Exists method
type ByteCache interface {
	// Get retrieves a value by key. Returns (nil, false) if not found or expired.
	Get(ctx context.Context, key string) ([]byte, bool)

	// Set stores a value with the given TTL.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration)

	// Delete removes a value by key.
	Delete(ctx context.Context, key string)
}
