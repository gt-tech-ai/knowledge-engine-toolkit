package interfaces

import "context"

// RateLimiter controls the rate of operations using a token bucket algorithm.
//
// Implementations: token bucket via golang.org/x/time/rate (default).
//
// All service code depends on this interface, never on a concrete rate
// limiter. Swap implementations via the ratelimiter.New factory.
type RateLimiter interface {
	// Allow reports whether an event may happen now (non-blocking).
	Allow() bool

	// Wait blocks until the limiter permits an event or ctx is cancelled.
	Wait(ctx context.Context) error
}
