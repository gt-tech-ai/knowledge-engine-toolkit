package interfaces

import "context"

// AdaptiveThrottler applies client-side adaptive throttling (Google SRE "Handling
// Overload"): it tracks the recent accept/reject ratio and, when a backend starts
// failing, rejects a growing fraction of outbound requests locally — shedding load before
// it reaches the struggling backend and easing off as the backend recovers. It composes
// with the retry budget: a locally-throttled request is a terminal rejection, not
// retried.
//
// A peer resilience contract of Retrier and Bulkhead: it wraps an operation with a policy
// and depends on no concrete implementation. Swap implementations via the
// adaptivethrottle.New factory.
type AdaptiveThrottler interface {
	// Do runs op unless the throttler decides to reject it locally, in which case it
	// returns a coded "throttled" rejection (see adaptivethrottle) WITHOUT invoking op.
	// Each op outcome (accepted/rejected by the backend) feeds the accept-rate window
	// that drives the decision.
	Do(ctx context.Context, op func() error) error
}
