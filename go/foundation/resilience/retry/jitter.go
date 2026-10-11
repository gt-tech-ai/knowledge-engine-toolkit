package retry

import (
	"math"
	"math/rand/v2"
	"time"
)

// EqualJitter applies equal jitter to a backoff duration: half of d plus a uniform
// random share of the other half, so the effective wait falls in [d/2, d]. It
// desynchronizes retries across callers recovering from the same outage (the AWS
// "equal jitter" strategy) while preserving the growth of the underlying backoff, so
// clients backing off from a shared dependency do not retry in lock-step. A
// non-positive duration is returned unchanged.
func EqualJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := d / 2
	return half + time.Duration(upTo(int64(half)))
}

// FullJitter returns a uniform random duration in [0, d] (the AWS "full jitter"
// strategy): the widest spread, for callers such as a polling relay whose retries
// must not cluster at all. A non-positive duration is returned unchanged.
func FullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return time.Duration(upTo(int64(d)))
}

// upTo draws uniformly from [0, n] for n >= 0; at math.MaxInt64 the bound n+1
// would overflow, so the full non-negative range is drawn directly.
func upTo(n int64) int64 {
	if n == math.MaxInt64 {
		return rand.Int64() //nolint:gosec // backoff desync, not security: a weak PRNG is fine.
	}
	//nolint:gosec // backoff desync, not a security decision — a weak PRNG is fine.
	return rand.Int64N(n + 1)
}
