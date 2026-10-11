package interfaces

import "context"

// Bulkhead limits concurrent access to a resource, preventing unbounded
// parallelism from exhausting goroutines, file descriptors, and memory.
//
// Implementations: channel semaphore (default).
//
// All service code depends on this interface, never on a concrete
// implementation. Swap implementations via the bulkhead.New factory.
type Bulkhead interface {
	// Execute runs fn within the concurrency limit. Blocks until a slot is
	// available or ctx is cancelled.
	Execute(ctx context.Context, fn func() error) error

	// TryExecute attempts to run fn without blocking. Returns an error if no
	// slots are available.
	TryExecute(fn func() error) error
}
