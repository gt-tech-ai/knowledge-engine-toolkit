package interfaces

import (
	"context"
	"time"
)

// Cache is the generic caching interface.
// T = value type.
type Cache[T any] interface {
	// Get retrieves a value by key. Returns (zero-value, false) if not found or expired.
	Get(ctx context.Context, key string) (T, bool)

	// Set stores a value with the given TTL. TTL of 0 means no expiration.
	Set(ctx context.Context, key string, value T, ttl time.Duration) error

	// Delete removes a value by key.
	Delete(ctx context.Context, key string) error

	// Exists checks if a key exists and is not expired.
	Exists(ctx context.Context, key string) bool
}

// CacheInvalidator provides cache invalidation capabilities.
type CacheInvalidator interface {
	// InvalidatePrefix removes all keys matching the given prefix.
	InvalidatePrefix(ctx context.Context, prefix string) error

	// InvalidateAll clears the entire cache.
	InvalidateAll(ctx context.Context) error
}
