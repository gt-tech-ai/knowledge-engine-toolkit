package interfaces

import "context"

// Hedger reduces tail latency by racing a backup attempt: it starts op, and if op has not
// returned within the hedge delay, starts a second concurrent op and takes whichever
// responds first (cancelling the loser). Only safe for IDEMPOTENT / read-only operations
// — a second attempt must not double a side effect — so callers opt in per operation.
//
// A peer resilience contract of Retrier and Bulkhead: like them it wraps an operation
// with a policy and depends on no concrete implementation. Swap implementations via the
// hedge.New factory.
type Hedger interface {
	// Hedge runs op, racing a second concurrent attempt after the configured delay, and
	// returns the result of the first attempt to complete. The slower attempt is cancelled
	// via ctx. Returns the first responder's error, or ctx's error if it is cancelled first.
	Hedge(ctx context.Context, op func() error) error
}
