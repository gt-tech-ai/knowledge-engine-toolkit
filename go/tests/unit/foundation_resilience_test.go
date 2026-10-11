package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/ratelimiter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// ---------------------------------------------------------------------------
// Circuit Breaker -- Factory Tests
// ---------------------------------------------------------------------------

// TestCircuitBreaker_FactoryReturnsInterface tests that the circuit breaker
// factory satisfies the interfaces.CircuitBreaker contract.
//
// Why this test is important:
//   - All resilience consumers depend on the factory to provide a valid
//     CircuitBreaker; a broken factory prevents services from starting
//   - Verifies the factory-to-interface wiring that enables implementation
//     swapping without consumer changes
//
// What it tests:
//   - circuitbreaker.New with KindGoBreaker returns a non-nil CircuitBreaker without
//     error
//   - Execute with a no-op function succeeds
func TestCircuitBreaker_FactoryReturnsInterface(t *testing.T) {
	t.Parallel()

	var cb interfaces.CircuitBreaker
	var err error
	cb, err = circuitbreaker.New(circuitbreaker.KindGoBreaker, "test")
	require.NoError(t, err, "unexpected error")

	require.NoError(t, cb.Execute(func() error { return nil }), "Execute must succeed")
}

// TestCircuitBreaker_OpensAfterConsecutiveFailures tests that the circuit
// breaker trips open after the configured failure threshold.
//
// Why this test is important:
//   - Circuit breaking is the primary defense against cascading failures across
//     services; a breaker that never opens would let failures propagate
//   - Protects downstream services from being overwhelmed during outages
//
// What it tests:
//   - After 3 consecutive failures the breaker transitions to open state
//   - Subsequent Execute calls fail immediately without invoking the operation
func TestCircuitBreaker_OpensAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	var cb interfaces.CircuitBreaker
	var err error
	cb, err = circuitbreaker.NewFromConfig(circuitbreaker.Config{
		Kind:                circuitbreaker.KindGoBreaker,
		Name:                "test-cb",
		MaxRequests:         1,
		Interval:            60 * time.Second,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 3,
	})
	require.NoError(t, err, "unexpected error")

	testErr := apperr.Sentinel("service unavailable")

	for range 3 {
		_ = cb.Execute(func() error {
			return testErr
		})
	}

	err = cb.Execute(func() error {
		return nil
	})
	require.Error(t, err, "expected circuit breaker to be open")
}

// TestCircuitBreaker_DomainErrorsDoNotTrip verifies that permanent domain errors
// (NotFound, Conflict) never open the breaker.
//
// Why this test is important:
//   - Before the IsSuccessful classifier, every error — including ordinary
//     business outcomes like NotFound — counted as a failure, so a burst of
//     "not found" lookups would trip the breaker and deny otherwise-healthy
//     traffic to the dependency.
//
// What it tests:
//   - After a burst of domain errors well past the consecutive-failure threshold,
//     the breaker stays closed: the next operation still runs.
func TestCircuitBreaker_DomainErrorsDoNotTrip(t *testing.T) {
	t.Parallel()

	cb, err := circuitbreaker.NewFromConfig(circuitbreaker.Config{
		Kind:                circuitbreaker.KindGoBreaker,
		Name:                "cb-domain",
		MaxRequests:         1,
		Interval:            time.Minute,
		Timeout:             time.Second,
		ConsecutiveFailures: 3,
		FailureRatio:        0.5,
		MinRequests:         100, // ratio guard inert for this small burst
	})
	require.NoError(t, err)

	for range 10 {
		_ = cb.Execute(func() error { return apperr.NotFound("gone") })
	}

	ran := false
	got := cb.Execute(func() error {
		ran = true
		return apperr.Conflict("dup")
	})
	require.True(
		t,
		ran,
		"breaker must stay closed for domain errors (operation still runs)",
	)
	require.Error(t, got, "the domain error still propagates")
}

// TestCircuitBreaker_FailureRatioTrips verifies the failure-ratio guard opens the
// breaker on a sustained error mix that never accumulates consecutive failures.
//
// Why this test is important:
//   - A dependency that fails ~half its calls interleaved with successes never
//     builds up ConsecutiveFailures, so a consecutive-only breaker would never
//     open despite the dependency being clearly unhealthy.
//
// What it tests:
//   - Alternating success/infra-failure at a 50% ratio (consecutive threshold set
//     very high) opens the breaker once the minimum request volume is reached.
func TestCircuitBreaker_FailureRatioTrips(t *testing.T) {
	t.Parallel()

	cb, err := circuitbreaker.NewFromConfig(circuitbreaker.Config{
		Kind:                circuitbreaker.KindGoBreaker,
		Name:                "cb-ratio",
		MaxRequests:         1,
		Interval:            time.Minute,
		Timeout:             time.Second,
		ConsecutiveFailures: 1000, // never trips on consecutive failures
		FailureRatio:        0.5,
		MinRequests:         10,
	})
	require.NoError(t, err)

	opened := false
	for i := range 60 {
		var opErr error
		if i%2 == 0 {
			opErr = apperr.Sentinel("unavailable") // infra failure (retryable)
		}
		ran := false
		_ = cb.Execute(func() error {
			ran = true
			return opErr
		})
		if !ran {
			opened = true // breaker rejected the call without running it
			break
		}
	}
	require.True(
		t,
		opened,
		"50%% failure ratio must open the breaker via the ratio guard",
	)
}

// TestCircuitBreaker_CallerCancellationDoesNotTrip verifies that caller-cancelled
// requests (context.Canceled) never open the breaker.
//
// Why this test is important:
//   - A cancellation reflects the caller going away (client disconnect, upstream
//     timeout), not an unhealthy dependency; counting it would let a burst of
//     client cancellations trip the breaker and deny otherwise-healthy traffic.
//
// What it tests:
//   - After a burst of context.Canceled past the consecutive-failure threshold,
//     the breaker stays closed: the next operation still runs.
func TestCircuitBreaker_CallerCancellationDoesNotTrip(t *testing.T) {
	t.Parallel()

	cb, err := circuitbreaker.NewFromConfig(circuitbreaker.Config{
		Kind:                circuitbreaker.KindGoBreaker,
		Name:                "cb-cancel",
		MaxRequests:         1,
		Interval:            time.Minute,
		Timeout:             time.Second,
		ConsecutiveFailures: 3,
		FailureRatio:        0.5,
		MinRequests:         100, // ratio guard inert for this small burst
	})
	require.NoError(t, err)

	for range 10 {
		_ = cb.Execute(func() error { return context.Canceled })
	}

	ran := false
	got := cb.Execute(func() error {
		ran = true
		return context.Canceled
	})
	require.True(
		t,
		ran,
		"breaker must stay closed for caller cancellations (operation still runs)",
	)
	require.ErrorIs(t, got, context.Canceled, "the cancellation still propagates")
}

// TestCircuitBreaker_DefaultConfig tests that the default circuit breaker
// config provides safe production values.
//
// Why this test is important:
//   - Production services that omit explicit config rely on these defaults; bad
//     defaults (e.g. threshold of 0) would either never trip or always trip
//   - Serves as a living specification for the default production behavior
//
// What it tests:
//   - Name matches the provided argument
//   - Kind is KindGoBreaker
//   - MaxRequests is 1 (allows one probe in half-open state)
//   - ConsecutiveFailures threshold is 5
func TestCircuitBreaker_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := circuitbreaker.DefaultConfig("my-breaker")
	assert.Equal(t, "my-breaker", cfg.Name)
	assert.Equal(t, circuitbreaker.KindGoBreaker, cfg.Kind)
	assert.Equal(t, uint32(1), cfg.MaxRequests)
	assert.Equal(t, uint32(5), cfg.ConsecutiveFailures)
}

// TestCircuitBreaker_UnknownKind tests that the factory rejects unsupported
// circuit breaker implementations.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup rather than produce a
//     nil breaker that panics on first Execute call
//
// What it tests:
//   - circuitbreaker.New with an unknown Kind returns a non-nil error
func TestCircuitBreaker_UnknownKind(t *testing.T) {
	t.Parallel()

	_, err := circuitbreaker.New(circuitbreaker.Kind(999), "test")
	require.Error(t, err, "expected error for unknown kind")
}

// TestCircuitBreaker_MockAsConsumerDependency tests that MockCircuitBreaker
// satisfies interfaces.CircuitBreaker so consumers can be tested in isolation.
//
// Why this test is important:
//   - Service-layer unit tests mock the circuit breaker; the mock must implement
//     the full interface or those tests cannot compile
//   - Validates the mock contract so upstream tests can trust it
//
// What it tests:
//   - MockCircuitBreaker assigned to interfaces.CircuitBreaker compiles
//   - Execute delegates to the mock and returns nil on success
func TestCircuitBreaker_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockCircuitBreaker(ctrl)

	mock.EXPECT().Execute(gomock.Any()).Return(nil)

	var cb interfaces.CircuitBreaker = mock
	require.NoError(t, cb.Execute(func() error { return nil }), "unexpected error")
}

// TestCircuitBreaker_MockReturnsError tests that the mock can simulate a
// tripped circuit breaker returning an open-state error.
//
// Why this test is important:
//   - Consumer tests must exercise the error path when the breaker is open;
//     this validates the mock can produce that scenario
//
// What it tests:
//   - Execute returns the configured open error from the mock
func TestCircuitBreaker_MockReturnsError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockCircuitBreaker(ctrl)

	openErr := apperr.Sentinel("circuit breaker is open")
	mock.EXPECT().Execute(gomock.Any()).Return(openErr)

	var cb interfaces.CircuitBreaker = mock
	err := cb.Execute(func() error { return nil })
	assert.ErrorIs(t, err, openErr)
}

// ---------------------------------------------------------------------------
// Retry Budget
// ---------------------------------------------------------------------------

// TestRetryBudget_BlocksWhenExhausted tests that the retry budget prevents
// unbounded retry storms by enforcing a hard cap.
//
// Why this test is important:
//   - Unbounded retries can amplify failures into retry storms that overwhelm
//     downstream services; the budget is the safety valve
//   - Without this, a single transient failure could trigger exponential load
//
// What it tests:
//   - First two consumptions succeed (budget=2)
//   - Third consumption returns false (budget exhausted)
func TestRetryBudget_BlocksWhenExhausted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := budget.NewBudget(2)
	ctx = budget.WithRetryBudget(ctx, b)

	require.True(t, budget.ConsumeRetry(ctx), "retry 1 should be allowed")
	require.True(t, budget.ConsumeRetry(ctx), "retry 2 should be allowed")

	assert.False(
		t,
		budget.ConsumeRetry(ctx),
		"retry 3 should be blocked (budget exhausted)",
	)
}

// TestRetryBudget_UnlimitedWithoutBudget tests that retries are unrestricted
// when no budget is attached to the context.
//
// Why this test is important:
//   - Existing callers that do not set a budget must continue to work without
//     sudden retry failures; backward compatibility is critical
//   - Prevents regressions when budget support is added to existing code paths
//
// What it tests:
//   - ConsumeRetry returns true for 100 consecutive calls on a bare context
func TestRetryBudget_UnlimitedWithoutBudget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for i := range 100 {
		assert.True(
			t,
			budget.ConsumeRetry(ctx),
			"retry %d should be allowed without budget",
			i,
		)
	}
}

// TestRetryBudget_Remaining tests that the remaining budget count accurately
// tracks consumption for observability.
//
// Why this test is important:
//   - Observability dashboards expose remaining budget to detect services
//     approaching retry exhaustion before they fail
//   - Incorrect tracking would mislead operators about system health
//
// What it tests:
//   - Remaining returns initial budget (3) before any consumption
//   - Remaining decrements to 2 after one ConsumeRetry call
func TestRetryBudget_Remaining(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := budget.NewBudget(3)
	ctx = budget.WithRetryBudget(ctx, b)

	assert.Equal(t, int32(3), budget.Remaining(ctx))

	budget.ConsumeRetry(ctx)
	assert.Equal(t, int32(2), budget.Remaining(ctx))
}

// ---------------------------------------------------------------------------
// Retry -- Factory Tests
// ---------------------------------------------------------------------------

// TestRetry_FactoryReturnsInterface tests that the retry factory satisfies the
// interfaces.Retrier contract.
//
// Why this test is important:
//   - All resilience consumers depend on the factory to provide a valid
//     Retrier; a broken factory prevents services from handling transient errors
//   - Verifies factory-to-interface wiring for implementation swapping
//
// What it tests:
//   - retry.New with KindExponential returns a non-nil Retrier without error
//   - Retry with a no-op function succeeds
func TestRetry_FactoryReturnsInterface(t *testing.T) {
	t.Parallel()

	var r interfaces.Retrier
	var err error
	r, err = retry.New(retry.KindExponential)
	require.NoError(t, err, "unexpected error")

	require.NoError(
		t,
		r.Retry(context.Background(), func() error { return nil }),
		"unexpected error",
	)
}

// TestRetry_SucceedsOnFirstAttempt tests that a successful first attempt
// incurs no retry overhead.
//
// Why this test is important:
//   - The common case is immediate success; retry machinery must not add
//     latency or invoke the operation more than once
//   - Extra invocations could cause duplicate side effects (e.g. double writes)
//
// What it tests:
//   - Operation is called exactly once
//   - Retry returns nil error
func TestRetry_SucceedsOnFirstAttempt(t *testing.T) {
	t.Parallel()

	var r interfaces.Retrier
	var err error
	r, err = retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     100 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  1 * time.Second,
	})
	require.NoError(t, err, "unexpected error")

	ctx := context.Background()
	var attempts atomic.Int32
	require.NoError(t, r.Retry(ctx, func() error {
		attempts.Add(1)
		return nil
	}), "unexpected error")
	assert.Equal(t, int32(1), attempts.Load(), "expected exactly 1 attempt")
}

// TestRetry_RetriesOnTransientError tests that transient failures trigger
// automatic retries until success with exponential backoff.
//
// Why this test is important:
//   - Transient errors (network blips, temporary unavailability) are common in
//     distributed systems; automatic retry is essential for reliability
//   - Validates the core retry loop that all services depend on for fault
//     tolerance
//
// What it tests:
//   - Operation fails twice then succeeds on the third attempt
//   - Total attempts equal 3
//   - Retry returns nil error after eventual success
func TestRetry_RetriesOnTransientError(t *testing.T) {
	t.Parallel()

	var r interfaces.Retrier
	var err error
	r, err = retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  1 * time.Second,
	})
	require.NoError(t, err, "unexpected error")

	ctx := context.Background()
	var attempts atomic.Int32
	require.NoError(t, r.Retry(ctx, func() error {
		if attempts.Add(1) < 3 {
			return apperr.Sentinel("transient")
		}
		return nil
	}), "unexpected error")
	assert.Equal(t, int32(3), attempts.Load(), "expected 3 total attempts")
}

// TestRetry_DoesNotRetryPermanentError tests that permanent domain errors
// (e.g. constraint conflicts) are surfaced immediately instead of retried.
//
// Why this test is important:
//   - Retrying a permanent failure (a unique/constraint violation) wastes time
//     and, inside a database transaction, masks the real cause behind
//     SQLSTATE 25P02 — the exact production bug this classifier prevents
//   - The real error must reach the caller so it maps to the correct status
//     (e.g. 409 Conflict) rather than a generic 500
//
// What it tests:
//   - An operation that returns a CodeConflict error runs exactly once
//   - Retry returns that original error unchanged
func TestRetry_DoesNotRetryPermanentError(t *testing.T) {
	t.Parallel()

	r, err := retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      3,
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     50 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  1 * time.Second,
	})
	require.NoError(t, err, "unexpected error")

	permanent := apperr.New(apperr.CodeConflict, "duplicate resource")
	var attempts atomic.Int32
	got := r.Retry(context.Background(), func() error {
		attempts.Add(1)
		return permanent
	})

	require.Error(t, got, "expected the permanent error to be returned")
	assert.Equal(
		t,
		apperr.CodeConflict,
		apperr.Code(got),
		"expected the real error code to survive",
	)
	assert.Equal(t, int32(1), attempts.Load(), "permanent error must not be retried")
}

// TestRetry_DefaultConfig tests that the default retry config provides safe
// production values for exponential backoff.
//
// Why this test is important:
//   - Services that omit explicit retry config inherit these defaults; wrong
//     values (e.g. MaxRetries=0) would silently disable retries
//   - Serves as a living specification for default retry behavior
//
// What it tests:
//   - Kind is KindExponential
//   - MaxRetries is 3
//   - InitialInterval is 100ms
//   - Multiplier is 2.0
func TestRetry_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := retry.DefaultConfig()
	assert.Equal(t, retry.KindExponential, cfg.Kind)
	assert.Equal(t, 3, cfg.MaxRetries)
	assert.Equal(t, 100*time.Millisecond, cfg.InitialInterval)
	assert.InDelta(t, 2.0, cfg.Multiplier, 0)
}

// TestRetry_UnknownKind tests that the factory rejects unsupported retry
// implementations.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup rather than produce
//     a nil retrier that panics on the first transient error
//
// What it tests:
//   - retry.New with an unknown Kind returns a non-nil error
func TestRetry_UnknownKind(t *testing.T) {
	t.Parallel()

	_, err := retry.New(retry.Kind(999))
	require.Error(t, err, "expected error for unknown kind")
}

// TestRetry_MockAsConsumerDependency tests that MockRetrier satisfies
// interfaces.Retrier so consumers can be tested in isolation.
//
// Why this test is important:
//   - Service-layer unit tests mock the retrier; the mock must implement the
//     full interface or those tests cannot compile
//   - Validates the mock contract so upstream tests can trust it
//
// What it tests:
//   - MockRetrier assigned to interfaces.Retrier compiles
//   - Retry delegates to the mock and returns nil on success
func TestRetry_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockRetrier(ctrl)

	mock.EXPECT().Retry(gomock.Any(), gomock.Any()).Return(nil)

	var r interfaces.Retrier = mock
	require.NoError(
		t,
		r.Retry(context.Background(), func() error { return nil }),
		"unexpected error",
	)
}

// TestRetry_MockReturnsError tests that the mock can simulate retry exhaustion
// returning a max-retries-exceeded error.
//
// Why this test is important:
//   - Consumer tests must exercise the retry-exhaustion path; this validates
//     the mock can produce that scenario for error-handling verification
//
// What it tests:
//   - Retry returns the configured error from the mock
func TestRetry_MockReturnsError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockRetrier(ctrl)

	retryErr := apperr.Sentinel("max retries exceeded")
	mock.EXPECT().Retry(gomock.Any(), gomock.Any()).Return(retryErr)

	var r interfaces.Retrier = mock
	err := r.Retry(context.Background(), func() error { return nil })
	assert.ErrorIs(t, err, retryErr)
}

// ---------------------------------------------------------------------------
// Bulkhead -- Factory Tests
// ---------------------------------------------------------------------------

// TestBulkhead_FactoryReturnsInterface tests that the bulkhead factory
// satisfies the interfaces.Bulkhead contract.
//
// Why this test is important:
//   - All concurrency-limited consumers depend on the factory to provide a
//     valid Bulkhead; a broken factory prevents resource isolation
//   - Verifies factory-to-interface wiring for implementation swapping
//
// What it tests:
//   - bulkhead.New with KindChannel returns a non-nil Bulkhead without error
//   - Execute with a no-op function succeeds
func TestBulkhead_FactoryReturnsInterface(t *testing.T) {
	t.Parallel()

	var bh interfaces.Bulkhead
	var err error
	bh, err = bulkhead.New(bulkhead.KindChannel)
	require.NoError(t, err, "unexpected error")

	require.NoError(
		t,
		bh.Execute(context.Background(), func() error { return nil }),
		"unexpected error",
	)
}

// TestBulkhead_LimitsConcurrency tests that the bulkhead enforces its
// concurrency cap, preventing resource exhaustion under load.
//
// Why this test is important:
//   - Without concurrency limits, a spike in requests could exhaust connection
//     pools, file descriptors, or memory, taking down the entire service
//   - The bulkhead pattern isolates failures to a bounded set of operations
//
// What it tests:
//   - Two concurrent operations fill both slots (MaxConcurrent=2)
//   - A third TryExecute call returns ErrBulkheadFull
func TestBulkhead_LimitsConcurrency(t *testing.T) {
	t.Parallel()

	var bh interfaces.Bulkhead
	var err error
	bh, err = bulkhead.NewFromConfig(bulkhead.Config{
		Kind:          bulkhead.KindChannel,
		MaxConcurrent: 2,
	})
	require.NoError(t, err, "unexpected error")

	blocker := make(chan struct{})
	for range 2 {
		go func() {
			_ = bh.Execute(context.Background(), func() error {
				<-blocker
				return nil
			})
		}()
	}

	time.Sleep(50 * time.Millisecond)

	err = bh.TryExecute(func() error { return nil })
	require.ErrorIs(t, err, bulkhead.ErrBulkheadFull)

	close(blocker)
}

// TestBulkhead_DefaultConfig tests that the default bulkhead config provides
// safe production concurrency limits.
//
// Why this test is important:
//   - Services that omit explicit bulkhead config inherit these defaults;
//     wrong values (e.g. MaxConcurrent=0) would block all operations
//   - Serves as a living specification for default concurrency behavior
//
// What it tests:
//   - Kind is KindChannel
//   - MaxConcurrent is 10
func TestBulkhead_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := bulkhead.DefaultConfig()
	assert.Equal(t, bulkhead.KindChannel, cfg.Kind)
	assert.Equal(t, 10, cfg.MaxConcurrent)
}

// TestBulkhead_UnknownKind tests that the factory rejects unsupported bulkhead
// implementations.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup rather than produce
//     a nil bulkhead that panics on the first Execute call
//
// What it tests:
//   - bulkhead.New with an unknown Kind returns a non-nil error
func TestBulkhead_UnknownKind(t *testing.T) {
	t.Parallel()

	_, err := bulkhead.New(bulkhead.Kind(999))
	require.Error(t, err, "expected error for unknown kind")
}

// TestBulkhead_MockAsConsumerDependency tests that MockBulkhead satisfies
// interfaces.Bulkhead so consumers can be tested in isolation.
//
// Why this test is important:
//   - Service-layer unit tests mock the bulkhead; the mock must implement the
//     full interface or those tests cannot compile
//   - Validates the mock contract so upstream tests can trust it
//
// What it tests:
//   - MockBulkhead assigned to interfaces.Bulkhead compiles
//   - Execute delegates to the mock and returns nil on success
func TestBulkhead_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockBulkhead(ctrl)

	mock.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil)

	var bh interfaces.Bulkhead = mock
	require.NoError(
		t,
		bh.Execute(context.Background(), func() error { return nil }),
		"unexpected error",
	)
}

// TestBulkhead_MockTryExecuteFull tests that the mock can simulate a fully
// occupied bulkhead returning a rejection error.
//
// Why this test is important:
//   - Consumer tests must exercise the rejection path when all slots are
//     occupied; this validates the mock can produce that scenario
//
// What it tests:
//   - TryExecute returns the configured full error from the mock
func TestBulkhead_MockTryExecuteFull(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockBulkhead(ctrl)

	fullErr := apperr.Sentinel("bulkhead full")
	mock.EXPECT().TryExecute(gomock.Any()).Return(fullErr)

	var bh interfaces.Bulkhead = mock
	err := bh.TryExecute(func() error { return nil })
	assert.ErrorIs(t, err, fullErr)
}

// ---------------------------------------------------------------------------
// Rate Limiter -- Factory Tests
// ---------------------------------------------------------------------------

// TestRateLimiter_FactoryReturnsInterface tests that the rate limiter factory
// satisfies the interfaces.RateLimiter contract.
//
// Why this test is important:
//   - API and ingestion services depend on rate limiting to enforce tenant
//     quotas; a broken factory would leave services unprotected from abuse
//   - Verifies factory-to-interface wiring for implementation swapping
//
// What it tests:
//   - ratelimiter.New with KindToken returns a non-nil RateLimiter without error
//   - Allow returns true for the first call
func TestRateLimiter_FactoryReturnsInterface(t *testing.T) {
	t.Parallel()

	var lim interfaces.RateLimiter
	var err error
	lim, err = ratelimiter.New(ratelimiter.KindToken)
	require.NoError(t, err, "unexpected error")

	require.True(t, lim.Allow(), "expected Allow() to return true")
}

// TestRateLimiter_AllowsWithinRate tests that requests within the configured
// burst capacity are not rejected.
//
// Why this test is important:
//   - Legitimate traffic bursts (e.g. page loads fetching multiple resources)
//     must not be throttled when within the configured burst window
//   - False rejections degrade user experience
//
// What it tests:
//   - All 10 Allow() calls succeed when burst=10 and rate=1000
func TestRateLimiter_AllowsWithinRate(t *testing.T) {
	t.Parallel()

	var lim interfaces.RateLimiter
	var err error
	lim, err = ratelimiter.NewFromConfig(ratelimiter.Config{
		Kind:  ratelimiter.KindToken,
		Rate:  1000,
		Burst: 10,
	})
	require.NoError(t, err, "unexpected error")

	for i := range 10 {
		assert.True(
			t,
			lim.Allow(),
			"Allow() returned false on attempt %d within burst",
			i,
		)
	}
}

// TestRateLimiter_RejectsOverBurst tests that requests exceeding the burst
// capacity are rejected to enforce traffic shaping.
//
// Why this test is important:
//   - Rate limiting protects downstream services and databases from overload;
//     a limiter that never rejects would provide no protection
//   - Ensures tenant isolation under load
//
// What it tests:
//   - Two Allow() calls consume the burst (burst=2)
//   - The third Allow() returns false
func TestRateLimiter_RejectsOverBurst(t *testing.T) {
	t.Parallel()

	var lim interfaces.RateLimiter
	var err error
	lim, err = ratelimiter.NewFromConfig(ratelimiter.Config{
		Kind:  ratelimiter.KindToken,
		Rate:  1,
		Burst: 2,
	})
	require.NoError(t, err, "unexpected error")

	lim.Allow()
	lim.Allow()

	assert.False(t, lim.Allow(), "expected Allow() to return false after burst exhausted")
}

// TestRateLimiter_WaitRespectsContext tests that Wait honors context
// cancellation rather than blocking indefinitely.
//
// Why this test is important:
//   - Request handlers carry deadlines; if Wait ignores context cancellation,
//     requests hang until the server timeout, wasting goroutines and sockets
//   - Proper cancellation propagation is essential for graceful shutdown
//
// What it tests:
//   - Wait with a cancelled context returns a non-nil error after the burst is consumed
func TestRateLimiter_WaitRespectsContext(t *testing.T) {
	t.Parallel()

	var lim interfaces.RateLimiter
	var err error
	lim, err = ratelimiter.NewFromConfig(ratelimiter.Config{
		Kind:  ratelimiter.KindToken,
		Rate:  1,
		Burst: 1,
	})
	require.NoError(t, err, "unexpected error")

	lim.Allow()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.Error(t, lim.Wait(ctx), "expected error from Wait with cancelled context")
}

// TestRateLimiter_DefaultConfig tests that the default rate limiter config
// provides safe production values.
//
// Why this test is important:
//   - Services that omit explicit rate limiter config inherit these defaults;
//     wrong values (e.g. Rate=0) would block all traffic
//   - Serves as a living specification for default throttling behavior
//
// What it tests:
//   - Kind is KindToken
//   - Rate is 100 requests per second
//   - Burst is 10
func TestRateLimiter_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := ratelimiter.DefaultConfig()
	assert.Equal(t, ratelimiter.KindToken, cfg.Kind)
	assert.InDelta(t, float64(100), cfg.Rate, 0)
	assert.Equal(t, 10, cfg.Burst)
}

// TestRateLimiter_UnknownKind tests that the factory rejects unsupported rate
// limiter implementations.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup rather than produce
//     a nil limiter that panics on the first Allow call
//
// What it tests:
//   - ratelimiter.New with an unknown Kind returns a non-nil error
func TestRateLimiter_UnknownKind(t *testing.T) {
	t.Parallel()

	_, err := ratelimiter.New(ratelimiter.Kind(999))
	require.Error(t, err, "expected error for unknown kind")
}

// TestRateLimiter_MockAsConsumerDependency tests that MockRateLimiter satisfies
// interfaces.RateLimiter so consumers can be tested in isolation.
//
// Why this test is important:
//   - Service-layer unit tests mock the rate limiter; the mock must implement
//     the full interface or those tests cannot compile
//   - Validates the mock contract so upstream tests can trust it
//
// What it tests:
//   - MockRateLimiter assigned to interfaces.RateLimiter compiles
//   - Allow returns true and Wait returns nil as configured
func TestRateLimiter_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockRateLimiter(ctrl)

	mock.EXPECT().Allow().Return(true)
	mock.EXPECT().Wait(gomock.Any()).Return(nil)

	var lim interfaces.RateLimiter = mock
	require.True(t, lim.Allow(), "expected Allow() to return true")
	require.NoError(t, lim.Wait(context.Background()), "unexpected error")
}

// TestRateLimiter_MockDeniesAccess tests that the mock can simulate
// rate-limited denial for consumer error-handling tests.
//
// Why this test is important:
//   - Consumer tests must exercise the throttled path; this validates the mock
//     can produce rate-limited denial for error-handling verification
//
// What it tests:
//   - Allow returns false as configured by the mock
func TestRateLimiter_MockDeniesAccess(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockRateLimiter(ctrl)

	mock.EXPECT().Allow().Return(false)

	var lim interfaces.RateLimiter = mock
	require.False(t, lim.Allow(), "expected Allow() to return false")
}
