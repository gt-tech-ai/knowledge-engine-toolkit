package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/adaptivethrottle"
)

// TestThrottler_RejectionRisesOnFailureAndEasesOnRecovery tests that the SRE rejection
// probability climbs while a backend fails and falls once it recovers.
//
// Why this test is important:
//   - Client-side throttling only protects a backend if the shed fraction tracks the
//     backend's health: rising while it rejects, easing as it recovers. A static
//     probability would either never protect or never let traffic back.
//
// What it tests:
//   - With shedding disabled (rand always allows), a healthy run keeps the probability at
//     zero, a failing run drives it positive, and a recovery run brings it back down.
func TestThrottler_RejectionRisesOnFailureAndEasesOnRecovery(t *testing.T) {
	t.Parallel()

	th := adaptivethrottle.New(adaptivethrottle.Config{
		K:     2.0,
		Decay: 1.0, // cumulative window → deterministic
		// never shed, so op always runs (isolates the ratio)
		Rand: func() float64 { return 1.0 },
	})
	ctx := context.Background()

	for range 10 { // healthy: accepts keep pace with requests → no rejection
		require.NoError(t, th.Do(ctx, func() error { return nil }))
	}
	require.Zero(t, th.RejectionProbability(), "a healthy backend is not throttled")

	for range 20 { // backend down: accepts frozen while requests climb → rejection rises
		require.Error(
			t,
			th.Do(ctx, func() error { return apperr.Sentinel("backend down") }),
		)
	}
	failProb := th.RejectionProbability()
	require.Positive(
		t,
		failProb,
		"sustained failure must raise the rejection probability",
	)

	for range 40 { // recovery: accepts climb again → rejection eases
		require.NoError(t, th.Do(ctx, func() error { return nil }))
	}
	require.Less(
		t,
		th.RejectionProbability(),
		failProb,
		"rejection eases as the backend recovers",
	)
}

// TestThrottler_ShedsLocallyWithoutCallingOp tests that once the probability is positive
// the throttler rejects a request locally without invoking op.
//
// Why this test is important:
//   - The protection is worthless unless a shed request actually skips the backend call;
//     if op still ran, the throttler would add latency without shedding load.
//
// What it tests:
//   - After one failure builds a positive probability, a request whose draw falls under
//     the probability returns ErrThrottled and never calls op.
func TestThrottler_ShedsLocallyWithoutCallingOp(t *testing.T) {
	t.Parallel()

	th := adaptivethrottle.New(adaptivethrottle.Config{
		K:     2.0,
		Decay: 1.0,
		Rand:  func() float64 { return 0.0 }, // draw 0 → shed whenever probability > 0
	})
	ctx := context.Background()

	// First call: probability is 0 at cold start, so it is allowed and its failure builds
	// p > 0.
	require.Error(t, th.Do(ctx, func() error { return apperr.Sentinel("backend down") }))

	called := false
	err := th.Do(ctx, func() error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, adaptivethrottle.ErrThrottled)
	require.False(t, called, "a shed request must not call op")
}

// TestThrottler_DefaultsCoerceInvalidConfig tests that New coerces out-of-range K/Decay
// (and a nil Rand) to safe defaults, so a zero/invalid Config yields a working throttler.
//
// Why this test is important:
//   - A mis-set config (K=0, no RNG) must not divide-by-zero or panic; coercing to SRE
//     defaults keeps the throttler safe rather than silently disabled or crashing.
//
// What it tests:
//   - New with a zero Config (and no Rand) built from DefaultConfig's intent leaves a
//     healthy backend un-throttled (probability zero) after a successful call.
func TestThrottler_DefaultsCoerceInvalidConfig(t *testing.T) {
	t.Parallel()

	_ = adaptivethrottle.DefaultConfig()
	th := adaptivethrottle.New(
		adaptivethrottle.Config{K: 0, Decay: 0},
	) // coerced to 2.0 / 0.98, default RNG

	require.NoError(t, th.Do(context.Background(), func() error { return nil }))
	require.Zero(
		t,
		th.RejectionProbability(),
		"a healthy backend under coerced defaults is not throttled",
	)
}
