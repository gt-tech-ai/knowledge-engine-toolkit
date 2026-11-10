package retry

import (
	"crypto/rand"
	"math/big"
	"time"
)

// jitterDivisor is halves a delay into its fixed and jittered parts.
const jitterDivisor = 2

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

	half := d / jitterDivisor

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

// upTo draws uniformly from [0, n] for n >= 0 using the operating system's entropy
// source, so no weak PRNG sits behind the retry desynchronisation. The bound n+1 is
// formed as a big integer, so it cannot overflow at math.MaxInt64. If the entropy
// source fails, the full delay n is used: the wait stays within its bound and the retry
// loses only its randomisation.
func upTo(n int64) int64 {
	bound := new(big.Int).Add(big.NewInt(n), big.NewInt(1))

	v, err := rand.Int(rand.Reader, bound)
	if err != nil {
		return n
	}

	return v.Int64()
}
