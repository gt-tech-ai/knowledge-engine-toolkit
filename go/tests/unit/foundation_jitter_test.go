package unit_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

// TestEqualJitter_RangeAndVariation tests that retry.EqualJitter keeps a backoff wait
// within [d/2, d] and actually varies across calls, so callers recovering from the
// same outage don't retry in lock-step.
//
// Why this test is important:
//   - Lock-step retries thunder-herd a recovering dependency; equal jitter spreads them
//     out. EqualJitter is a shared resilience primitive (used by the SQS receive loop and
//     available to any backoff caller), so its range and non-determinism must hold.
//
// What it tests:
//   - EqualJitter(d) is always within [d/2, d]; over many calls it produces more than one
//     distinct value (not deterministic); a non-positive backoff is returned unchanged.
func TestEqualJitter_RangeAndVariation(t *testing.T) {
	t.Parallel()

	const d = 800 * time.Millisecond
	seen := make(map[time.Duration]struct{})
	for range 200 {
		j := retry.EqualJitter(d)
		require.GreaterOrEqual(t, j, d/2, "jitter must be >= d/2 (equal jitter floor)")
		require.LessOrEqual(t, j, d, "jitter must be <= d")
		seen[j] = struct{}{}
	}
	require.Greater(t, len(seen), 1, "jitter must vary across calls (no lock-step)")
	require.Zero(t, retry.EqualJitter(0), "a zero backoff is returned unchanged")
	require.Equal(
		t,
		-5*time.Second,
		retry.EqualJitter(-5*time.Second),
		"a negative backoff is returned unchanged",
	)
}

// TestFullJitter_RangeAndVariation tests FullJitter's uniform [0, d] draw.
//
// Why this test is important:
//   - The outbox relay schedules retries with full jitter; a draw outside
//     [0, d] would exceed the backoff cap or schedule a retry in the past
//
// What it tests:
//   - 200 draws for 800ms all fall in [0, 800ms] and are not all equal
//   - zero and negative durations are returned unchanged
func TestFullJitter_RangeAndVariation(t *testing.T) {
	t.Parallel()

	const d = 800 * time.Millisecond
	seen := make(map[time.Duration]struct{})
	for range 200 {
		j := retry.FullJitter(d)
		require.GreaterOrEqual(t, j, time.Duration(0))
		require.LessOrEqual(t, j, d)
		seen[j] = struct{}{}
	}
	require.Greater(t, len(seen), 1)
	require.Zero(t, retry.FullJitter(0))
	require.Equal(t, -5*time.Second, retry.FullJitter(-5*time.Second))
}

// TestJitter_MaxDurationDoesNotOverflow tests both jitters at the largest duration.
//
// Why this test is important:
//   - A caller with an uncapped exponential backoff can reach the largest
//     time.Duration; drawing from [0, d] there must not overflow d+1 into a
//     negative bound, which panics the retrying goroutine
//
// What it tests:
//   - FullJitter(MaxInt64) and EqualJitter(MaxInt64) return without panicking,
//     within [0, MaxInt64] and [MaxInt64/2, MaxInt64] respectively
func TestJitter_MaxDurationDoesNotOverflow(t *testing.T) {
	t.Parallel()
	maxD := time.Duration(math.MaxInt64)

	for range 100 {
		full := retry.FullJitter(maxD)
		require.GreaterOrEqual(t, full, time.Duration(0))
		equal := retry.EqualJitter(maxD)
		require.GreaterOrEqual(t, equal, maxD/2)
	}
}
