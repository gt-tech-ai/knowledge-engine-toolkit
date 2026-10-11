package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestInterceptorCoreRetry tests that interceptorcore.Retry classifies a call's
// outcome — permanent vs exhausted vs success — the way both interceptor stacks
// relied on before the shared core existed.
//
// Why this test is important:
//   - The gRPC and Connect retry interceptors now delegate their classification
//     to this one function; a regression here (e.g. retrying a permanent error, or
//     conflating "gave up" with "terminal") would silently change the retry
//     behavior of every RPC in both stacks at once.
//
// What it tests:
//   - A transient error that resolves → both returns nil (success), and the op ran
//     more than once; a permanent error → returned as `permanent`, not retried;
//     an always-failing transient error → returned as `exhausted`, permanent nil.
func TestInterceptorCoreRetry(t *testing.T) {
	t.Parallel()
	sentinelPermanent := apperr.Sentinel("permanent")
	sentinelTransient := apperr.Sentinel("transient")
	isPermanent := func(err error) bool { return apperr.StdIs(err, sentinelPermanent) }

	t.Run("transient then success", func(t *testing.T) {
		t.Parallel()
		retrier, _ := fixtures.StubRetrier(2, false)
		calls := 0
		permanent, exhausted := interceptorcore.Retry(
			context.Background(), retrier, isPermanent,
			func() error {
				calls++
				if calls <= 2 {
					return sentinelTransient
				}
				return nil
			},
		)
		require.NoError(t, permanent)
		require.NoError(t, exhausted)
		require.Equal(t, 3, calls, "op retried until it succeeded")
	})

	t.Run("permanent error is not retried", func(t *testing.T) {
		t.Parallel()
		retrier, _ := fixtures.StubRetrier(5, false)
		calls := 0
		permanent, exhausted := interceptorcore.Retry(
			context.Background(), retrier, isPermanent,
			func() error {
				calls++
				return sentinelPermanent
			},
		)
		require.ErrorIs(t, permanent, sentinelPermanent)
		require.NoError(t, exhausted)
		require.Equal(t, 1, calls, "a permanent error stops after one attempt")
	})

	t.Run("exhausted transient error", func(t *testing.T) {
		t.Parallel()
		retrier, _ := fixtures.StubRetrier(0, true)
		permanent, exhausted := interceptorcore.Retry(
			context.Background(), retrier, isPermanent,
			func() error { return sentinelTransient },
		)
		require.NoError(t, permanent)
		require.ErrorIs(t, exhausted, sentinelTransient)
	})
}

// TestInterceptorCoreCircuitBreak tests that interceptorcore.CircuitBreak reports
// an open-circuit rejection distinctly from the call's own error.
//
// Why this test is important:
//   - Both stacks map `rejected` to their framework's Unavailable and pass the
//     inner error through untouched; if a rejected call were mistaken for a call
//     that ran (or vice versa) the breaker would either leak an "open" error as a
//     real failure or mask a real failure as an open circuit.
//
// What it tests:
//   - Open breaker → rejected=true, call never runs; closed breaker + call error →
//     rejected=false with the inner error; closed breaker + success → both zero.
func TestInterceptorCoreCircuitBreak(t *testing.T) {
	t.Parallel()
	sentinel := apperr.Sentinel("inner failure")

	t.Run("open circuit rejects before running", func(t *testing.T) {
		t.Parallel()
		cb := fixtures.StubCircuitBreaker(true)
		ran := false
		rejected, innerErr := interceptorcore.CircuitBreak(cb, func() error {
			ran = true
			return nil
		})
		require.True(t, rejected)
		require.NoError(t, innerErr)
		require.False(t, ran, "an open circuit must not invoke the call")
	})

	t.Run("closed circuit propagates inner error", func(t *testing.T) {
		t.Parallel()
		cb := fixtures.StubCircuitBreaker(false)
		rejected, innerErr := interceptorcore.CircuitBreak(
			cb,
			func() error { return sentinel },
		)
		require.False(t, rejected)
		require.ErrorIs(t, innerErr, sentinel)
	})

	t.Run("closed circuit success", func(t *testing.T) {
		t.Parallel()
		cb := fixtures.StubCircuitBreaker(false)
		rejected, innerErr := interceptorcore.CircuitBreak(
			cb,
			func() error { return nil },
		)
		require.False(t, rejected)
		require.NoError(t, innerErr)
	})
}

// TestInterceptorCoreBulkhead tests that interceptorcore.Bulkhead reports a load
// shed (call never ran) distinctly from the call running and erroring.
//
// Why this test is important:
//   - Both stacks map ran=false to ResourceExhausted; if a shed were reported as
//     ran=true the adapter would return the handler's (nil) result as a success
//     instead of shedding, defeating the backpressure the bulkhead exists to apply.
//
// What it tests:
//   - A free slot → ran=true with the call's own error; a full bulkhead (its only
//     slot held) → ran=false with the shed error and the call never invoked.
func TestInterceptorCoreBulkhead(t *testing.T) {
	t.Parallel()
	sentinel := apperr.Sentinel("handler failure")

	t.Run("free slot runs the call", func(t *testing.T) {
		t.Parallel()
		bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
		require.NoError(t, err)
		ran, callErr := interceptorcore.Bulkhead(bh, func() error { return sentinel })
		require.True(t, ran)
		require.ErrorIs(t, callErr, sentinel)
	})

	t.Run("sheds when the only slot is held", func(t *testing.T) {
		t.Parallel()
		bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
		require.NoError(t, err)

		holding := make(chan struct{})
		release := make(chan struct{})
		go func() {
			_ = bh.TryExecute(func() error {
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding // the goroutine now occupies the single slot

		called := false
		ran, shedErr := interceptorcore.Bulkhead(bh, func() error {
			called = true
			return nil
		})
		close(release)
		require.False(t, ran, "the call is shed when no slot is free")
		require.Error(t, shedErr)
		require.False(t, called, "a shed call must never run")
	})
}

// TestInterceptorCoreClassifyTimeout tests that ClassifyTimeout distinguishes the
// interceptor's own deadline from an upstream cancellation and from neither.
//
// Why this test is important:
//   - Both timeout interceptors relabel errors off this classification; mislabeling
//     a caller-side cancel as a full deadline (or vice versa) corrupts latency
//     diagnosis, which is the exact failure the split guards against.
//
// What it tests:
//   - A deadline-exceeded ctx → TimeoutDeadline; a canceled ctx → TimeoutCanceled;
//     a live ctx → TimeoutNone.
func TestInterceptorCoreClassifyTimeout(t *testing.T) {
	t.Parallel()

	t.Run("deadline exceeded", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		<-ctx.Done()
		require.Equal(
			t,
			interceptorcore.TimeoutDeadline,
			interceptorcore.ClassifyTimeout(ctx),
		)
	})

	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.Equal(
			t,
			interceptorcore.TimeoutCanceled,
			interceptorcore.ClassifyTimeout(ctx),
		)
	})

	t.Run("live context", func(t *testing.T) {
		t.Parallel()
		require.Equal(
			t,
			interceptorcore.TimeoutNone,
			interceptorcore.ClassifyTimeout(context.Background()),
		)
	})
}
