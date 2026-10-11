package unit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	decorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

// errBoom is a transient failure used to drive retry / circuit-breaker paths.
var errBoom = apperr.Sentinel("boom")

// fastRetrier builds a real Retrier with tiny intervals so retry tests run in
// milliseconds rather than seconds.
func fastRetrier(t *testing.T, maxRetries int) *decorators.Stack {
	t.Helper()
	r, err := retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      maxRetries,
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  time.Second,
	})
	require.NoError(t, err)
	return decorators.New("test").WithRetrier(r)
}

// TestClientStack_RetriesTransientThenSucceeds tests that a Retryable operation
// which fails transiently then succeeds is retried by the stack's Retry layer.
//
// Why this test is important:
//   - The widest-blast-radius direct-SDK calls must survive transient blips; the
//     Retry layer is the mechanism, and a Retryable:false op must NOT be retried.
//
// What it tests:
//   - A fn failing once then succeeding is invoked twice and returns success when
//     RunOpts.Retryable is true.
func TestClientStack_RetriesTransientThenSucceeds(t *testing.T) {
	t.Parallel()

	s := fastRetrier(t, 3)
	var calls int32
	out, err := decorators.Run(context.Background(), s, "get",
		decorators.RunOpts{Retryable: true},
		func(context.Context) (string, error) {
			if atomic.AddInt32(&calls, 1) == 1 {
				return "", errBoom
			}
			return "ok", nil
		})

	require.NoError(t, err)
	assert.Equal(t, "ok", out)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "should retry once then succeed")
}

// TestClientStack_NonRetryableIsNotRetried tests that a Retryable:false op is
// invoked exactly once even when it fails, so body-consuming ops (S3 Upload) are
// never replayed.
//
// Why this test is important:
//   - Replaying a body-consuming operation corrupts data; the per-op Retryable
//     flag is the guard.
//
// What it tests:
//   - A failing fn with RunOpts.Retryable false is invoked exactly once.
func TestClientStack_NonRetryableIsNotRetried(t *testing.T) {
	t.Parallel()

	s := fastRetrier(t, 3)
	var calls int32
	_, err := decorators.Run(context.Background(), s, "upload",
		decorators.RunOpts{Retryable: false},
		func(context.Context) (string, error) {
			atomic.AddInt32(&calls, 1)
			return "", errBoom
		})

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "non-retryable op must run once")
}

// TestClientStack_CircuitOpensAfterThreshold tests that the circuit breaker trips
// after repeated failures and then fails fast without invoking the wrapped fn.
//
// Why this test is important:
//   - Failing fast when a downstream is down is the point of the breaker; without
//     it a dead dependency ties up every caller.
//
// What it tests:
//   - After the failure threshold is crossed, a subsequent call returns an error
//     without invoking fn.
func TestClientStack_CircuitOpensAfterThreshold(t *testing.T) {
	t.Parallel()

	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "test-cb",
		circuitbreaker.WithMaxRequests(1))
	require.NoError(t, err)
	s := decorators.New("test").WithCircuitBreaker(cb)

	// Trip the breaker: gobreaker's default ReadyToTrip opens after consecutive
	// failures; drive enough failing calls to open it.
	for range 10 {
		_, _ = decorators.Run(context.Background(), s, "op", decorators.RunOpts{},
			func(context.Context) (string, error) { return "", errBoom })
	}

	var invoked bool
	_, err = decorators.Run(context.Background(), s, "op", decorators.RunOpts{},
		func(context.Context) (string, error) { invoked = true; return "ok", nil })

	require.Error(t, err, "open breaker should fail fast")
	assert.False(t, invoked, "open breaker must not invoke fn")
}

// TestClientStack_RetryAttemptsEachHitCircuitBreaker tests the load-bearing
// nesting order: Retry is OUTSIDE the CircuitBreaker, so each retry attempt is
// observed by the breaker, and once it opens mid-retry the remaining attempts
// fail fast without re-invoking fn.
//
// Why this test is important:
//   - If CB wrapped the whole retry loop instead of each attempt, the breaker
//     would see one aggregate outcome and never shed load between attempts —
//     inverting the prescribed Bulkhead→Retry→CircuitBreaker order.
//
// What it tests:
//   - With a low CB threshold and a higher retry budget, an always-failing fn is
//     invoked only up to the threshold (fewer than MaxRetries+1), proving the CB
//     short-circuits attempts inside the retry loop.
func TestClientStack_RetryAttemptsEachHitCircuitBreaker(t *testing.T) {
	t.Parallel()

	r, err := retry.NewFromConfig(retry.Config{
		Kind: retry.KindExponential, MaxRetries: 8,
		InitialInterval: time.Millisecond, MaxInterval: 2 * time.Millisecond,
		Multiplier: 2.0, MaxElapsedTime: time.Second,
	})
	require.NoError(t, err)
	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "nest-cb")
	require.NoError(t, err)
	s := decorators.New("test").WithRetrier(r).WithCircuitBreaker(cb)

	var calls int32
	_, err = decorators.Run(context.Background(), s, "op",
		decorators.RunOpts{Retryable: true},
		func(context.Context) (string, error) {
			atomic.AddInt32(&calls, 1)
			return "", errBoom
		})

	require.Error(t, err)
	// gobreaker's default ReadyToTrip opens after >5 consecutive failures, so fn is
	// invoked ~6 times before the breaker short-circuits the remaining retries. A snug
	// upper bound (7) still catches the inverted-nesting regression (which would invoke
	// fn all 9 attempts) AND a subtler one where the CB opens several attempts too late.
	assert.LessOrEqual(
		t,
		atomic.LoadInt32(&calls),
		int32(7),
		"CB should open after ~6 failures and stop re-invoking fn well before MaxRetries+1",
	)
}

// TestClientStack_BulkheadLimitsConcurrency tests that the bulkhead bounds the
// number of concurrently in-flight operations.
//
// Why this test is important:
//   - Unbounded parallelism against a slow dependency exhausts goroutines/FDs;
//     the bulkhead is the cap.
//
// What it tests:
//   - With MaxConcurrent=1, two concurrent Runs never overlap (peak in-flight 1).
func TestClientStack_BulkheadLimitsConcurrency(t *testing.T) {
	t.Parallel()

	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
	require.NoError(t, err)
	s := decorators.New("test").WithBulkhead(bh)

	var inFlight, peak int32
	var wg sync.WaitGroup
	// stackOp is the operation signature decorators.Run takes.
	type stackOp func(context.Context) (string, error)
	var op stackOp = func(context.Context) (string, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return "ok", nil
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = decorators.Run(context.Background(), s, "op", decorators.RunOpts{}, op)
		}()
	}
	wg.Wait()

	assert.Equal(
		t,
		int32(1),
		atomic.LoadInt32(&peak),
		"bulkhead must cap concurrency at 1",
	)
}

// TestClientStack_PreservesConfiguredTimeout tests that the Timeout layer applies
// the configured deadline verbatim and never imposes a shorter default.
//
// Why this test is important:
//   - A deliberate long timeout (e.g. Ollama's 300s embed) must not be silently
//     shortened by the stack — the AC forbids it.
//
// What it tests:
//   - The fn observes a context deadline within tolerance of the configured
//     timeout; a stack with zero timeout imposes no deadline.
func TestClientStack_PreservesConfiguredTimeout(t *testing.T) {
	t.Parallel()

	// Configured long timeout is passed through, not shortened.
	s := decorators.New("test").WithTimeout(90 * time.Second)
	_, err := decorators.Run(context.Background(), s, "op", decorators.RunOpts{},
		func(ctx context.Context) (string, error) {
			dl, ok := ctx.Deadline()
			require.True(t, ok, "timeout layer should set a deadline")
			assert.InDelta(
				t,
				90*time.Second,
				time.Until(dl),
				float64(2*time.Second),
				"deadline must match configured timeout",
			)
			return "ok", nil
		})
	require.NoError(t, err)

	// Zero timeout imposes no deadline.
	s0 := decorators.New("test")
	_, err = decorators.Run(context.Background(), s0, "op", decorators.RunOpts{},
		func(ctx context.Context) (string, error) {
			_, ok := ctx.Deadline()
			assert.False(t, ok, "zero timeout must not impose a deadline")
			return "ok", nil
		})
	require.NoError(t, err)
}

// TestClientStack_DisabledConfigIsPassthrough tests that a disabled Config yields
// a bare stack that invokes fn directly with no resilience layers.
//
// Why this test is important:
//   - The AC requires a backwards-compatible path: config off → bare client.
//
// What it tests:
//   - StackFromConfig(Enabled:false) returns a stack that runs fn once with no
//     imposed deadline (no timeout layer) and returns its result verbatim.
func TestClientStack_DisabledConfigIsPassthrough(t *testing.T) {
	t.Parallel()

	s, err := decorators.StackFromConfig(
		"test",
		decorators.Config{Enabled: false},
		decorators.Deps{},
	)
	require.NoError(t, err)

	var calls int32
	out, err := decorators.Run(
		context.Background(),
		s,
		"op",
		decorators.RunOpts{Retryable: true},
		func(ctx context.Context) (string, error) {
			atomic.AddInt32(&calls, 1)
			_, ok := ctx.Deadline()
			assert.False(t, ok, "disabled stack imposes no timeout")
			return "bare", nil
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "bare", out)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}
