package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/adaptive"
)

// TestAdaptiveBulkhead_LimitShrinksUnderLatencyAndRecovers tests that the AIMD limit
// shrinks under sustained latency and grows back once latency subsides.
//
// Why this test is important:
//   - The whole point of an adaptive limiter is to back off concurrency at a slowing
//     backend and reclaim it on recovery; if the limit did not move with latency it would
//     be a fixed bulkhead with extra machinery.
//
// What it tests:
//   - Four slow samples (RTT over the threshold) drive the limit below its initial value;
//     five fast samples then grow it back above the shrunk value.
func TestAdaptiveBulkhead_LimitShrinksUnderLatencyAndRecovers(t *testing.T) {
	t.Parallel()

	bh := adaptive.New(adaptive.Config{
		MinConcurrent:     1,
		MaxConcurrent:     20,
		InitialConcurrent: 10,
		RTTThreshold:      10 * time.Millisecond,
		BackoffRatio:      0.5,
	})
	require.Equal(t, 10, bh.Limit())

	for range 4 { // slow samples: RTT over the threshold shrinks the limit multiplicatively
		require.NoError(t, bh.Execute(context.Background(), func() error {
			time.Sleep(20 * time.Millisecond)
			return nil
		}))
	}
	shrunk := bh.Limit()
	require.Less(t, shrunk, 10, "sustained latency must shrink the adaptive limit")

	for range 5 { // fast samples: RTT under the threshold grows the limit back additively
		require.NoError(t, bh.Execute(context.Background(), func() error { return nil }))
	}
	require.Greater(t, bh.Limit(), shrunk, "the limit recovers once latency subsides")
}

// TestAdaptiveBulkhead_TryExecuteRejectsWhenFull tests that TryExecute rejects
// immediately once the current adaptive limit is saturated (the non-blocking contract of
// Bulkhead).
//
// Why this test is important:
//   - TryExecute exists so a caller can shed rather than queue; it must reject the moment
//     the limit is reached, or the "try" semantics are broken.
//
// What it tests:
//   - With a limit of 1 occupied by an in-flight Execute, a concurrent TryExecute returns
//     ErrBulkheadFull.
func TestAdaptiveBulkhead_TryExecuteRejectsWhenFull(t *testing.T) {
	t.Parallel()

	bh := adaptive.New(adaptive.Config{
		MinConcurrent:     1,
		MaxConcurrent:     1,
		InitialConcurrent: 1,
		RTTThreshold:      time.Hour, // never treat the held slot as a slow sample
		BackoffRatio:      0.9,
	})
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = bh.Execute(context.Background(), func() error {
			close(started)
			<-release
			return nil
		})
	}()

	<-started // the single slot is now occupied
	require.ErrorIs(
		t,
		bh.TryExecute(func() error { return nil }),
		adaptive.ErrBulkheadFull,
	)
	close(release)
}

// TestBulkhead_KindAdaptive_Selected tests that the factory selects the adaptive backend
// for KindAdaptive (the config-selects-impl contract,
// ARCHITECTURE.md#swappable-components).
//
// Why this test is important:
//   - The adaptive limiter is opted into by a config Kind; if NewFromConfig silently
//     returned a channel bulkhead, the configured self-tuning behavior would never take
//     effect.
//
// What it tests:
//   - bulkhead.New(KindAdaptive) returns an *adaptive.Bulkhead.
func TestBulkhead_KindAdaptive_Selected(t *testing.T) {
	t.Parallel()

	bh, err := bulkhead.New(bulkhead.KindAdaptive)
	require.NoError(t, err)
	_, ok := bh.(*adaptive.Bulkhead)
	require.True(t, ok, "KindAdaptive must select the adaptive backend")
}

// TestAdaptiveBulkhead_TryExecuteRunsWhenSlotAvailable tests that TryExecute runs the op
// and returns its result when a slot is free (the non-rejecting path), using the package
// defaults.
//
// Why this test is important:
//   - TryExecute must actually execute when capacity exists, not only reject when full;
//     the happy path is the common case and a regression there would silently drop work.
//
// What it tests:
//   - With a fresh default-config bulkhead, TryExecute invokes the op and returns nil.
func TestAdaptiveBulkhead_TryExecuteRunsWhenSlotAvailable(t *testing.T) {
	t.Parallel()

	bh := adaptive.New(adaptive.DefaultConfig())
	called := false
	require.NoError(t, bh.TryExecute(func() error {
		called = true
		return nil
	}))
	require.True(t, called, "TryExecute must run the op when a slot is available")
}

// TestAdaptiveBulkhead_ExecuteRespectsContextCancellation tests that a blocked Execute
// returns the context error when its context is cancelled while waiting for a slot (the
// ctx-aware acquire path).
//
// Why this test is important:
//   - A caller that cancels (deadline, shutdown) must not hang forever waiting for a
//     saturated bulkhead; honoring ctx during the wait is what makes the limiter safe
//     under load.
//
// What it tests:
//   - With the single slot held, a second Execute blocks in acquire and returns
//     context.Canceled once its context is cancelled.
func TestAdaptiveBulkhead_ExecuteRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	bh := adaptive.New(adaptive.Config{
		MinConcurrent:     1,
		MaxConcurrent:     1,
		InitialConcurrent: 1,
		RTTThreshold:      time.Hour,
		BackoffRatio:      0.9,
	})
	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = bh.Execute(context.Background(), func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started // the single slot is now held

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bh.Execute(ctx, func() error { return nil }) }()
	cancel() // the blocked acquire must observe the cancellation and return

	require.ErrorIs(t, <-done, context.Canceled)
	close(release)
}
