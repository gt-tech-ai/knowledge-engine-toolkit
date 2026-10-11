package interfaces

import "context"

// Retrier executes operations with automatic retry on transient failures.
//
// Implementations: exponential backoff with jitter (default).
//
// All service code depends on this interface, never on a concrete retry
// library. Swap implementations via the retry.New factory.
type Retrier interface {
	// Retry executes op with automatic retry on transient failures.
	// Returns nil on success, or the last error after all retries are exhausted.
	Retry(ctx context.Context, op func() error) error
}
