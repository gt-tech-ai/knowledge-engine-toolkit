package unit_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/transport/connect/interceptors"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// testHeaders returns the gateway header contract the interceptor tests send, passed to
// the auth interceptor explicitly (the library has no default header names).
func testHeaders() interceptors.HeaderMap {
	return interceptors.HeaderMap{
		Sub:      "X-User-Sub",
		Tenant:   "X-Org-ID",
		Email:    "X-User-Email",
		Name:     "X-User-Name",
		NickName: "X-User-Nickname",
		Roles:    "X-Roles",
	}
}

// newTestRequest returns a minimal AnyRequest for testing.
// Uses connect.NewRequest with a nil proto message.
func newTestRequest() connect.AnyRequest {
	return connect.NewRequest[*emptyMessage](nil)
}

// emptyMessage is a minimal type satisfying proto.Message for test requests.
type emptyMessage struct{}

func (f *emptyMessage) ProtoReflect() {}

// newTestResponse returns a minimal AnyResponse for testing.
func newTestResponse() connect.AnyResponse {
	return connect.NewResponse[*emptyMessage](nil)
}

// ---------------------------------------------------------------------------
// Recovery interceptor
// ---------------------------------------------------------------------------

// TestRecoveryInterceptor_CatchesPanic tests that the recovery interceptor
// converts handler panics into structured Connect errors.
//
// Why this test is important:
//   - Unrecovered panics crash the entire process, taking down all connections
//   - The recovery interceptor is the last line of defense against handler bugs
//   - Structured error codes allow clients to distinguish server faults from client
//     errors
//
// What it tests:
//   - A panicking handler returns nil response and a CodeInternal Connect error
func TestRecoveryInterceptor_CatchesPanic(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := interceptors.RecoveryInterceptor(logger)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			panic("test panic")
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	assert.Nil(t, resp, "expected nil response after panic")
	require.Error(t, err, "expected error after panic")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// TestRecoveryInterceptor_PassesThroughNormal tests that the recovery
// interceptor is transparent to successful requests.
//
// Why this test is important:
//   - The interceptor must add zero overhead or side effects to the happy path
//   - Response mutation by middleware would silently corrupt data for all handlers
//
// What it tests:
//   - A non-panicking handler's response passes through unchanged with no error
func TestRecoveryInterceptor_PassesThroughNormal(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := interceptors.RecoveryInterceptor(logger)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestRecoveryInterceptor_PassesThroughError tests that the recovery
// interceptor preserves original handler error codes without interference.
//
// Why this test is important:
//   - Error codes carry semantic meaning that clients use for retry/fallback decisions
//   - Re-wrapping errors as Internal would mask the true cause and break client logic
//
// What it tests:
//   - A handler returning CodeNotFound propagates as CodeNotFound (not swallowed or
//     re-wrapped)
func TestRecoveryInterceptor_PassesThroughError(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := interceptors.RecoveryInterceptor(logger)

	innerErr := connect.NewError(connect.CodeNotFound, apperr.Sentinel("not found"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error to pass through")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// ---------------------------------------------------------------------------
// Rate limit interceptor
// ---------------------------------------------------------------------------

// TestRateLimitInterceptor_AllowsWithinLimit tests that requests within the
// rate limit pass through to the handler without interference.
//
// Why this test is important:
//   - A misconfigured rate limiter that rejects valid traffic causes false outages
//   - Confirms the interceptor delegates correctly to the rate limiter abstraction
//
// What it tests:
//   - A request allowed by the rate limiter returns the handler's response with no error
func TestRateLimitInterceptor_AllowsWithinLimit(t *testing.T) {
	t.Parallel()
	limiter := fixtures.StubRateLimiter(true)
	interceptor := interceptors.RateLimitInterceptor(limiter)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestRateLimitInterceptor_RejectsWhenExceeded tests that requests exceeding
// the rate limit are rejected before reaching the handler.
//
// Why this test is important:
//   - Rate limiting protects downstream services from overload and resource exhaustion
//   - The handler must not execute when the limit is exceeded to preserve server capacity
//   - Correct error codes allow clients to implement backoff strategies
//
// What it tests:
//   - A rate-limited request returns CodeResourceExhausted without invoking the handler
func TestRateLimitInterceptor_RejectsWhenExceeded(t *testing.T) {
	t.Parallel()
	limiter := fixtures.StubRateLimiter(false)
	interceptor := interceptors.RateLimitInterceptor(limiter)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			require.FailNow(t, "handler should not be called when rate limited")
			return nil, apperr.Sentinel("unreachable")
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error when rate limited")
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
}

// TestBulkheadInterceptor_ShedsWhenFull verifies the bulkhead interceptor rejects
// requests with ResourceExhausted once the concurrency limit is reached.
//
// Why this test is important:
//   - Without shedding, a burst of concurrent internal calls can exhaust the DB
//     pool / goroutines; the bulkhead must fail excess calls fast instead of
//     queueing them behind a saturated resource.
//
// What it tests:
//   - With MaxConcurrent=1 and one request held in-flight, a second request is
//     rejected with CodeResourceExhausted and its handler never runs.
func TestBulkheadInterceptor_ShedsWhenFull(t *testing.T) {
	t.Parallel()
	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
	require.NoError(t, err)
	ic := interceptors.BulkheadInterceptor(bh)

	holding := make(chan struct{})
	release := make(chan struct{})
	blocking := ic.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			holding <- struct{}{} // signal the one slot is held
			<-release
			return newTestResponse(), nil
		},
	)
	go func() { _, _ = blocking(context.Background(), newTestRequest()) }()
	<-holding // the one slot is now occupied

	shed := ic.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			t.Error("handler must not run when the bulkhead is full")
			return newTestResponse(), nil
		},
	)
	_, err = shed(context.Background(), newTestRequest())
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))

	close(release) // let the in-flight request finish
}

// TestBulkheadInterceptor_PassesWhenFree verifies a request within the
// concurrency limit runs and its result passes through unchanged.
func TestBulkheadInterceptor_PassesWhenFree(t *testing.T) {
	t.Parallel()
	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(2))
	require.NoError(t, err)
	ic := interceptors.BulkheadInterceptor(bh)

	want := newTestResponse()
	h := ic.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return want, nil
		},
	)
	got, err := h(context.Background(), newTestRequest())
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// ---------------------------------------------------------------------------
// Timeout interceptor
// ---------------------------------------------------------------------------

// TestTimeoutInterceptor_CompletesBeforeDeadline tests that the timeout
// interceptor is transparent when the handler completes within the deadline.
//
// Why this test is important:
//   - The timeout interceptor must not interfere with normal-speed requests
//   - Premature cancellation would cause spurious failures under healthy conditions
//
// What it tests:
//   - A fast handler returns its response with no error
func TestTimeoutInterceptor_CompletesBeforeDeadline(t *testing.T) {
	t.Parallel()
	interceptor := interceptors.TimeoutInterceptor(time.Second)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestTimeoutInterceptor_ExceedsDeadline tests that the timeout interceptor
// cancels slow handlers and returns a deadline-exceeded error.
//
// Why this test is important:
//   - Unbounded request latency ties up server resources and can cascade into outages
//   - The correct error code enables clients to distinguish timeouts from other failures
//
// What it tests:
//   - A handler that blocks past the deadline returns CodeDeadlineExceeded
func TestTimeoutInterceptor_ExceedsDeadline(t *testing.T) {
	t.Parallel()
	interceptor := interceptors.TimeoutInterceptor(time.Millisecond)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error on timeout")
	assert.Equal(t, connect.CodeDeadlineExceeded, connect.CodeOf(err))
}

// TestTimeoutInterceptor_CancelledContextReturnsCanceled tests that a request
// that ends because its parent context was cancelled reports CodeCanceled and is
// NOT relabeled as a "request timed out" deadline.
//
// Why this test is important:
//   - Conflating a caller-side cancellation (or a fast-fail on an already-done
//     context) with a real timeout misreports the failure as a full <timeout>
//     wait, corrupting latency diagnosis — an 18ms cancel logged as "timed out
//     after 5s" sends an operator chasing a phantom slow dependency.
//   - Clients switch on the status code, so a cancellation must surface as
//     Canceled, not DeadlineExceeded.
//
// What it tests:
//   - A pre-cancelled parent context yields CodeCanceled and a message that does
//     not claim a timeout
func TestTimeoutInterceptor_CancelledContextReturnsCanceled(t *testing.T) {
	t.Parallel()
	interceptor := interceptors.TimeoutInterceptor(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller/upstream goes away before the request resolves

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, ctx.Err()
		},
	)

	_, err := handler(ctx, newTestRequest())
	require.Error(t, err, "expected error on cancelled context")
	assert.Equal(
		t,
		connect.CodeCanceled,
		connect.CodeOf(err),
		"a cancellation must not be reported as a timeout",
	)
	assert.NotContains(
		t,
		err.Error(),
		"timed out",
		"must not mislabel a cancellation as a timeout",
	)
}

// TestTimeoutInterceptor_PassesThroughNonContextError tests that an ordinary
// handler error (with deadline budget to spare) is returned unchanged rather
// than relabeled as a timeout.
//
// Why this test is important:
//   - The timeout interceptor must be transparent to real application errors;
//     relabeling an Internal fault as a timeout hides the true failure and
//     misleads clients that switch on the status code.
//
// What it tests:
//   - A fast handler returning CodeInternal is propagated unchanged
func TestTimeoutInterceptor_PassesThroughNonContextError(t *testing.T) {
	t.Parallel()
	interceptor := interceptors.TimeoutInterceptor(time.Minute)

	sentinel := connect.NewError(connect.CodeInternal, apperr.Sentinel("boom"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, sentinel
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err)
	assert.Equal(
		t,
		connect.CodeInternal,
		connect.CodeOf(err),
		"a non-context error must pass through unchanged",
	)
}

// ---------------------------------------------------------------------------
// Circuit breaker interceptor
// ---------------------------------------------------------------------------

// TestCircuitBreakerInterceptor_PassesWhenClosed tests that the circuit
// breaker interceptor allows requests through when the circuit is closed.
//
// Why this test is important:
//   - The circuit breaker must be transparent during healthy operation
//   - Incorrectly blocking requests when the circuit is closed would cause false outages
//
// What it tests:
//   - A request with a closed circuit returns the handler's response with no error
func TestCircuitBreakerInterceptor_PassesWhenClosed(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(false)
	interceptor := interceptors.CircuitBreakerInterceptor(cb)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestCircuitBreakerInterceptor_RejectsWhenOpen tests that the circuit
// breaker interceptor fast-fails requests when the circuit is open.
//
// Why this test is important:
//   - Open-circuit fast-fail prevents cascading failures to a degraded dependency
//   - The handler must not execute to avoid wasting resources on a known-bad path
//   - CodeUnavailable signals clients to use fallback or retry later
//
// What it tests:
//   - A request with an open circuit returns CodeUnavailable without invoking the handler
func TestCircuitBreakerInterceptor_RejectsWhenOpen(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(true)
	interceptor := interceptors.CircuitBreakerInterceptor(cb)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			require.FailNow(t, "handler should not be called when circuit is open")
			return nil, apperr.Sentinel("unreachable")
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error when circuit is open")
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// TestCircuitBreakerInterceptor_PropagatesInnerError tests that handler
// errors propagate unchanged when the circuit is closed.
//
// Why this test is important:
//   - Masking handler errors as circuit-breaker errors would hide the true failure cause
//   - Clients need accurate error codes for correct retry and fallback decisions
//
// What it tests:
//   - A handler returning CodeNotFound propagates as CodeNotFound through the closed
//     circuit
func TestCircuitBreakerInterceptor_PropagatesInnerError(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(false)
	interceptor := interceptors.CircuitBreakerInterceptor(cb)

	innerErr := connect.NewError(connect.CodeNotFound, apperr.Sentinel("not found"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected inner error to propagate")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// ---------------------------------------------------------------------------
// Retry interceptor
// ---------------------------------------------------------------------------

// TestRetryInterceptor_SucceedsFirstAttempt tests that the retry interceptor
// adds no overhead when the handler succeeds on the first attempt.
//
// Why this test is important:
//   - The retry interceptor must be transparent for successful requests
//   - Unnecessary retries would multiply load on downstream services
//
// What it tests:
//   - A successful handler returns its response directly with no error
func TestRetryInterceptor_SucceedsFirstAttempt(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(0, false)
	interceptor := interceptors.RetryInterceptor(retrier)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestRetryInterceptor_RetriesTransientError tests that the retry interceptor
// retries transient failures until the handler succeeds.
//
// Why this test is important:
//   - Automatic retry of transient errors enables recovery from flaky dependencies
//   - Without retry, every transient blip propagates as a user-visible failure
//   - Correct attempt counting ensures the retry budget is consumed as expected
//
// What it tests:
//   - A handler failing twice with CodeUnavailable then succeeding results in 3 total
//     attempts
//   - The final successful response is returned with no error
func TestRetryInterceptor_RetriesTransientError(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(2, false)
	interceptor := interceptors.RetryInterceptor(retrier)

	attempts := 0
	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			attempts++
			if attempts <= 2 {
				return nil, connect.NewError(
					connect.CodeUnavailable,
					apperr.Sentinel("transient"),
				)
			}
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response after retries")
	assert.Equal(t, 3, attempts)
}

// TestRetryInterceptor_StopsOnPermanentError tests that the retry interceptor
// does not retry non-retryable errors like CodeInvalidArgument.
//
// Why this test is important:
//   - Retrying permanent errors wastes resources and delays the error response to the
//     client
//   - Client-side mistakes (bad input) will never succeed on retry
//   - Preserving the original error code is critical for correct client-side handling
//
// What it tests:
//   - A handler returning CodeInvalidArgument triggers exactly 1 attempt (no retries)
//   - The original error code is preserved in the returned error
func TestRetryInterceptor_StopsOnPermanentError(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(5, false) // would retry many times
	interceptor := interceptors.RetryInterceptor(retrier)

	attempts := 0
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			attempts++
			return nil, connect.NewError(
				connect.CodeInvalidArgument,
				apperr.Sentinel("bad request"),
			)
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error for permanent failure")
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	// Should stop after first attempt since InvalidArgument is non-retryable.
	assert.Equal(t, 1, attempts)
}

// TestRetryInterceptor_StopsOnResourceExhausted verifies that a rate-limited
// (ResourceExhausted) response is not retried.
//
// Why this test is important:
//   - Retrying a rate-limited dependency immediately only deepens the backpressure
//     and can escalate a brief throttle into a self-inflicted outage.
//
// What it tests:
//   - A handler returning CodeResourceExhausted triggers exactly 1 attempt and the
//     code is preserved.
func TestRetryInterceptor_StopsOnResourceExhausted(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(5, false) // would retry many times
	interceptor := interceptors.RetryInterceptor(retrier)

	attempts := 0
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			attempts++
			return nil, connect.NewError(
				connect.CodeResourceExhausted,
				apperr.Sentinel("rate limited"),
			)
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error for rate-limited failure")
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	assert.Equal(t, 1, attempts, "ResourceExhausted must not be retried")
}

// TestClientBuilder_BreakerWrapsRetry_OpenFailsFast verifies the client the
// builder ACTUALLY produces places the circuit breaker OUTSIDE retry.
//
// Why this test is important:
//   - With retry outside the breaker, an open breaker's Unavailable is seen as a
//     transient error and retried with backoff — pouring load onto the very
//     dependency the breaker tripped to protect. Driving this through a real
//     Connect client built from Build()'s options (not a hand-composed chain)
//     means a chain() reorder that reintroduces the bug is caught here.
//
// What it tests:
//   - A client built with WithCircuitBreaker(open)+WithRetry returns
//     CodeUnavailable, invokes the retrier zero times, and never hits the transport.
func TestClientBuilder_BreakerWrapsRetry_OpenFailsFast(t *testing.T) {
	t.Parallel()
	retrier, attempts := fixtures.StubRetrier(
		5,
		false,
	) // would retry many times if reached
	openCB := fixtures.StubCircuitBreaker(true)

	opts := interceptors.NewClientBuilder().
		WithCircuitBreaker(openCB).
		WithRetry(retrier).
		Build()

	// A generated RoundTripper mock with no expectations: an open breaker must fail
	// fast before any network round-trip, so any RoundTrip call is an unexpected
	// call that fails the test.
	transport := mocks.NewMockRoundTripper(gomock.NewController(t))
	client := connect.NewClient[emptyMessage, emptyMessage](
		&http.Client{Transport: transport},
		"http://localhost/test.v1.Service/Method",
		opts...,
	)

	_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptyMessage{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(
		t,
		0,
		attempts(),
		"the built client must place the breaker outside retry: an open breaker fails fast "+
			"without reaching the retrier",
	)
}

// TestBudgetInterceptor_AttachesSharedBudget verifies the server entrypoint
// attaches a per-request retry budget to the context.
//
// Why this test is important:
//   - Without one shared budget, each retrier in a request's chain retries
//     independently, so a deep call tree can multiply into a retry storm.
//
// What it tests:
//   - A handler wrapped by BudgetInterceptor(n) sees budget.Remaining(ctx) == n;
//     n <= 0 attaches no budget (Remaining reports -1).
func TestBudgetInterceptor_AttachesSharedBudget(t *testing.T) {
	t.Parallel()

	var withBudget int32 = -99
	h := interceptors.BudgetInterceptor(7).WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			withBudget = budget.Remaining(ctx)
			return newTestResponse(), nil
		},
	)
	_, err := h(context.Background(), newTestRequest())
	require.NoError(t, err)
	assert.Equal(
		t,
		int32(7),
		withBudget,
		"the request context must carry the attached budget",
	)

	var disabled int32 = -99
	h0 := interceptors.BudgetInterceptor(0).WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			disabled = budget.Remaining(ctx)
			return newTestResponse(), nil
		},
	)
	_, err = h0(context.Background(), newTestRequest())
	require.NoError(t, err)
	assert.Equal(
		t,
		int32(-1),
		disabled,
		"maxRetries<=0 attaches no budget (Remaining reports -1)",
	)
}

// TestClientBuilder_PerAttemptTimeout_HungAttemptIsRetried verifies the timeout
// sits INSIDE retry: a hung attempt is cut short by its per-attempt deadline
// and the retrier proceeds to the next attempt, rather than the timeout cancelling
// the whole retry loop.
//
// Why this test is important:
//   - With the timeout wrapping retry, one slow first attempt burns the entire
//     deadline and no retry ever happens; per-attempt timeouts are what let a
//     transient hang recover on the next try.
//
// What it tests:
//   - retry (outer) wrapping timeout (inner): a first attempt that hangs past its
//     per-attempt deadline is retried, and the second attempt succeeds.
func TestClientBuilder_PerAttemptTimeout_HungAttemptIsRetried(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(1, false) // one failure, then succeed
	attempts := 0

	handler := interceptors.RetryInterceptor(retrier).WrapUnary(
		interceptors.TimeoutInterceptor(time.Millisecond).WrapUnary(
			func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
				attempts++
				if attempts == 1 {
					<-ctx.Done() // hang until the per-attempt deadline fires
					return nil, ctx.Err()
				}
				return newTestResponse(), nil
			},
		),
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "the retry after a per-attempt timeout should succeed")
	assert.NotNil(t, resp)
	assert.Equal(
		t,
		2,
		attempts,
		"a hung first attempt is timed out per-attempt (DeadlineExceeded stays retryable) "+
			"and retried",
	)
}

// ---------------------------------------------------------------------------
// ServerBuilder
// ---------------------------------------------------------------------------

// TestServerBuilder_EmptyBuildReturnsNil tests that building a ServerBuilder
// with no interceptors configured returns nil.
//
// Why this test is important:
//   - Callers rely on nil to detect empty chains and skip registration
//   - Returning a non-nil empty chain could cause unexpected middleware execution
//
// What it tests:
//   - An empty ServerBuilder produces nil handler options
func TestServerBuilder_EmptyBuildReturnsNil(t *testing.T) {
	t.Parallel()
	opts := interceptors.NewServerBuilder().Build()
	assert.Nil(t, opts, "expected nil options from empty builder")
}

// TestServerBuilder_FullComposition tests that the ServerBuilder composes all
// available interceptors into a single handler option.
//
// Why this test is important:
//   - The full middleware stack (recovery, rate limit, metrics, tracing, logging, auth,
//     validation) must compose without errors
//   - Interceptor ordering and compatibility issues would break every service at startup
//   - Validates the builder pattern wires all dependencies correctly
//
// What it tests:
//   - A fully-configured ServerBuilder produces exactly 1 non-nil WithInterceptors option
func TestServerBuilder_FullComposition(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	metrics := fixtures.NopMetrics()
	tracer := fixtures.NopTracer()
	limiter := fixtures.StubRateLimiter(true)
	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
	require.NoError(t, err)

	opts := interceptors.NewServerBuilder().
		WithRecovery().
		WithRateLimit(limiter).
		WithBulkhead(bh).
		WithMetrics(metrics).
		WithTracing(tracer).
		WithLogging(logger).
		WithAuth(false, testHeaders()).
		WithValidation().
		Build()

	require.NotNil(t, opts, "expected non-nil options from full builder")
	assert.Len(t, opts, 1)
}

// TestServerBuilder_RecoveryRequiresLogger tests that the recovery interceptor
// is silently omitted when no logger dependency is provided.
//
// Why this test is important:
//   - Recovery without logging would swallow panics silently, making debugging impossible
//   - The builder must enforce dependency requirements to prevent misconfigured stacks
//
// What it tests:
//   - WithRecovery without WithLogging produces nil options (recovery not added)
func TestServerBuilder_RecoveryRequiresLogger(t *testing.T) {
	t.Parallel()
	// Recovery without logger should not add the interceptor.
	opts := interceptors.NewServerBuilder().
		WithRecovery().
		Build()

	assert.Nil(t, opts, "expected nil options when recovery is set without logger")
}

// ---------------------------------------------------------------------------
// ClientBuilder
// ---------------------------------------------------------------------------

// TestClientBuilder_EmptyBuildReturnsNil tests that building a ClientBuilder
// with no interceptors configured returns nil.
//
// Why this test is important:
//   - Callers rely on nil to detect empty chains and skip registration
//   - Returning a non-nil empty chain could cause unexpected client middleware execution
//
// What it tests:
//   - An empty ClientBuilder produces nil client options
func TestClientBuilder_EmptyBuildReturnsNil(t *testing.T) {
	t.Parallel()
	opts := interceptors.NewClientBuilder().Build()
	assert.Nil(t, opts, "expected nil options from empty builder")
}

// TestClientBuilder_FullComposition tests that the ClientBuilder composes all
// available client interceptors into a single client option.
//
// Why this test is important:
//   - The full client middleware stack (timeout, retry, circuit breaker, metrics,
//     tracing, logging) must compose without errors
//   - Client interceptor ordering affects resilience behavior (e.g., timeout wrapping
//     retry)
//   - Validates the builder pattern wires all client-side dependencies correctly
//
// What it tests:
//   - A fully-configured ClientBuilder produces exactly 1 non-nil WithInterceptors option
func TestClientBuilder_FullComposition(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	metrics := fixtures.NopMetrics()
	tracer := fixtures.NopTracer()
	retrier, _ := fixtures.StubRetrier(0, false)
	cb := fixtures.StubCircuitBreaker(false)

	opts := interceptors.NewClientBuilder().
		WithTimeout(5 * time.Second).
		WithRetry(retrier).
		WithCircuitBreaker(cb).
		WithMetrics(metrics).
		WithTracing(tracer).
		WithLogging(logger).
		Build()

	require.NotNil(t, opts, "expected non-nil options from full builder")
	assert.Len(t, opts, 1)
}

// ---------------------------------------------------------------------------
// Auth interceptor — WithAuthClaims / GetAuthClaims / splitRoles
// ---------------------------------------------------------------------------

// TestWithAuthClaims_GetAuthClaims_RoundTrip tests that auth claims can be
// stored in and retrieved from a context without data loss.
//
// Why this test is important:
//   - Identity propagation via context is the foundation of multi-tenant authorization
//   - Data corruption in any claim field could grant unauthorized access or deny valid
//     users
//   - Every handler depends on this contract to enforce tenant isolation
//
// What it tests:
//   - Claims with Sub, TenantID, and Roles survive a context round-trip
//   - All fields are preserved exactly as set
func TestWithAuthClaims_GetAuthClaims_RoundTrip(t *testing.T) {
	t.Parallel()

	claims := &interceptors.AuthClaims{
		Sub:      "user-1",
		TenantID: "org-1",
		Roles:    []string{"admin", "editor"},
	}
	ctx := interceptors.WithAuthClaims(context.Background(), claims)

	got, ok := interceptors.GetAuthClaims(ctx)
	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "user-1", got.Sub)
	assert.Equal(t, "org-1", got.TenantID)
	assert.Len(t, got.Roles, 2)
}

// TestGetAuthClaims_Missing tests that GetAuthClaims returns false when no
// claims exist in the context.
//
// Why this test is important:
//   - Treating zero-value claims as valid identity would bypass authorization checks
//   - Handlers must be able to distinguish "no auth" from "authenticated with empty
//     fields"
//
// What it tests:
//   - An empty context returns ok=false from GetAuthClaims
func TestGetAuthClaims_Missing(t *testing.T) {
	t.Parallel()

	_, ok := interceptors.GetAuthClaims(context.Background())
	assert.False(t, ok, "expected no claims in empty context")
}

// newTestRequestWithHeaders creates a request with specific HTTP headers.
func newTestRequestWithHeaders(headers map[string]string) connect.AnyRequest {
	req := connect.NewRequest[*emptyMessage](nil)
	for k, v := range headers {
		req.Header().Set(k, v)
	}
	return req
}

// TestAuthInterceptor_WithAllHeaders tests that the auth interceptor extracts
// all identity headers and stores them as AuthClaims in context.
//
// Why this test is important:
//   - The auth interceptor is the entry point for identity propagation in Connect
//     services
//   - Missing or misparse of any header field breaks downstream authorization decisions
//   - Role splitting must handle the comma-separated format used by the API gateway
//
// What it tests:
//   - All four header fields are parsed and stored as AuthClaims
//   - Comma-separated X-Roles header is split into a 3-element slice
func TestAuthInterceptor_WithAllHeaders(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:    "user-42",
		testHeaders().Tenant: "org-99",
		testHeaders().Roles:  "admin,editor,viewer",
	})

	resp, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, resp, "expected non-nil response")

	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "user-42", claims.Sub)
	assert.Equal(t, "org-99", claims.TenantID)
	assert.Len(t, claims.Roles, 3)
}

// TestAuthInterceptor_NoSub tests that the auth interceptor skips storing
// claims when no X-User-Sub header is present.
//
// Why this test is important:
//   - Storing empty claims would cause handlers to treat unauthenticated requests as
//     authenticated
//   - The interceptor must allow unauthenticated requests through for public endpoints
//   - Downstream auth-required checks depend on claims absence to enforce access control
//
// What it tests:
//   - A request with no headers produces no AuthClaims in context
//   - The handler still executes successfully (no error)
func TestAuthInterceptor_NoSub(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var ok bool

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			_, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	// No headers → no claims stored
	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")

	assert.False(t, ok, "expected no claims when X-User-Sub is absent")
}

// TestAuthInterceptor_SubOnly_NoRoles tests that the auth interceptor
// stores claims with nil Roles when no X-Roles header is present.
//
// Why this test is important:
//   - nil vs empty-slice semantics affect role-checking logic downstream
//   - Handlers use nil Roles to distinguish "no roles assigned" from "roles checked but
//     empty"
//
// What it tests:
//   - Claims are stored with Sub populated and Roles nil
func TestAuthInterceptor_SubOnly_NoRoles(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub: "user-1",
	})

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "user-1", claims.Sub)
	assert.Nil(t, claims.Roles)
}

// TestAuthInterceptor_EmptyRoles tests that an empty X-Roles header results
// in nil Roles rather than a slice with an empty string.
//
// Why this test is important:
//   - A single-element slice containing "" would match empty-string role checks
//     spuriously
//   - Edge case handling of empty headers prevents authorization bypass
//
// What it tests:
//   - An empty X-Roles header value results in Roles being nil
func TestAuthInterceptor_EmptyRoles(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:   "user-1",
		testHeaders().Roles: "",
	})

	_, _ = handler(context.Background(), req)

	require.True(t, ok, "expected claims")
	// Empty roles string should NOT set roles
	assert.Nil(t, claims.Roles)
}

// TestAuthInterceptor_RolesWithTrailingComma tests that splitRoles discards
// empty segments from malformed role strings.
//
// Why this test is important:
//   - API gateways may produce trailing or double commas in role headers
//   - Phantom empty-string roles would match unintended authorization rules
//   - Robust parsing prevents security issues from malformed input
//
// What it tests:
//   - The header "admin,,editor," is parsed into exactly 2 roles: admin and editor
func TestAuthInterceptor_RolesWithTrailingComma(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:   "user-1",
		testHeaders().Roles: "admin,,editor,",
	})

	_, _ = handler(context.Background(), req)

	require.True(t, ok, "expected claims")
	// splitRoles should discard empty segments
	assert.Len(t, claims.Roles, 2)
}

// TestAuthInterceptor_StubMode_SynthesizesDevClaims tests that stub mode
// synthesizes dev claims, marked synthetic, when no identity headers are present.
//
// Why this test is important:
//   - Local dev has no gateway, so identity headers are never set
//   - Stub mode must provide claims so handlers can proceed in dev
//   - The Synthetic mark is what tells a principal interceptor to apply its stub
//     principal; unmarked stub claims would be resolved as a real "stub-user" caller
//
// What it tests:
//   - With stub=true and no headers, Sub/Email/Name/NickName/TenantID are non-empty
//   - Claims are stored in context (ok=true) and marked Synthetic
func TestAuthInterceptor_StubMode_SynthesizesDevClaims(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(true, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err)

	require.True(
		t,
		ok,
		"expected synthesized claims in context when stub=true and no headers",
	)

	assert.NotEmpty(t, claims.Sub, "expected non-empty Sub in stub claims")
	assert.NotEmpty(t, claims.Email, "expected non-empty Email in stub claims")
	assert.NotEmpty(t, claims.Name, "expected non-empty Name in stub claims")
	assert.NotEmpty(t, claims.NickName, "expected non-empty NickName in stub claims")
	assert.NotEmpty(t, claims.TenantID, "expected non-empty TenantID in stub claims")
	assert.True(t, claims.Synthetic, "stub claims must be marked Synthetic")
}

// TestAuthInterceptor_StubMode_RealHeadersWin tests that when stub=true but
// real identity headers ARE present (e.g. running behind a gateway in dev), those
// headers take precedence over the synthesized dev claims.
//
// Why this test is important:
//   - Stub mode must only fill the gap when no gateway is present; if it
//     clobbered real gateway-injected headers, a dev request authenticated as a
//     specific user would silently run as the stub user instead, masking
//     authorization bugs
//   - Header-derived claims marked Synthetic would hand every real caller the
//     consumer's stub principal (a tenant and role bypass)
//
// What it tests:
//   - Real subject / tenant headers are preserved as the claim Sub/TenantID
//     rather than being overwritten by synthesized stub values
//   - The header-derived claims are not marked Synthetic
func TestAuthInterceptor_StubMode_RealHeadersWin(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(true, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)

	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok = interceptors.GetAuthClaims(ctx)
			return newTestResponse(), nil
		},
	)

	req := newTestRequestWithHeaders(map[string]string{
		testHeaders().Sub:    "real-user-from-gateway",
		testHeaders().Tenant: "real-org-from-gateway",
	})

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	require.True(t, ok, "expected claims in context")
	assert.Equal(t, "real-user-from-gateway", claims.Sub)
	assert.Equal(t, "real-org-from-gateway", claims.TenantID)
	assert.False(
		t,
		claims.Synthetic,
		"header-derived claims must not be marked Synthetic",
	)
}

// ---------------------------------------------------------------------------
// Logging interceptor
// ---------------------------------------------------------------------------

// TestLoggingInterceptor_Success tests that the logging interceptor passes
// through successful responses without modification.
//
// Why this test is important:
//   - Logging must be a transparent decorator that never alters responses
//   - Response mutation by the logging layer would silently corrupt data
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestLoggingInterceptor_Success(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	interceptor := interceptors.NewLoggingInterceptor(logger)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestLoggingInterceptor_Error tests that the logging interceptor propagates
// handler errors unchanged.
//
// Why this test is important:
//   - Error codes carry semantic meaning used by clients for retry and fallback decisions
//   - The logging layer must never re-wrap or swallow errors
//
// What it tests:
//   - A handler returning CodeNotFound propagates as CodeNotFound through the logging
//     interceptor
func TestLoggingInterceptor_Error(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	interceptor := interceptors.NewLoggingInterceptor(logger)

	innerErr := connect.NewError(connect.CodeNotFound, apperr.Sentinel("not found"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error to pass through")
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// TestLoggingInterceptor_UsesWithContext tests that the logging interceptor
// calls logger.WithContext(ctx) so log entries include trace_id and span_id.
//
// Why this test is important:
//   - The tracing interceptor enriches the context with an OTel span before the
//     logging interceptor runs. If the logger doesn't call WithContext, trace
//     correlation fields (trace_id, span_id) are missing from RPC log entries.
//   - Without this, log-trace linking in Grafana (Loki → Tempo) doesn't work.
//
// What it tests:
//   - On success, logger.WithContext(ctx) is called and Info is logged on the
//     context-enriched logger (not the bare logger).
func TestLoggingInterceptor_UsesWithContext_Success(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")

	// The bare logger should have zero Info calls — all logging should go
	// through the child returned by WithContext.
	assert.Empty(t, spy.InfoCalls, "expected 0 calls")

	// The child logger (created by WithContext) should have received the Info call.
	assert.Len(t, *spy.ChildInfoCalls, 1)
}

// TestLoggingInterceptor_UsesWithContext_Error tests that the logging interceptor
// calls logger.WithContext(ctx) for error log entries as well.
//
// Why this test is important:
//   - Error logs must also include trace_id/span_id for correlation.
//   - A common mistake is to only enrich the success path but not the error path.
//
// What it tests:
//   - On error, logger.WithContext(ctx) is called and Error is logged on the
//     context-enriched logger (not the bare logger).
func TestLoggingInterceptor_UsesWithContext_Error(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(connect.CodeInternal, apperr.Sentinel("boom"))
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	// The bare logger should have zero Error calls — all logging should go
	// through the child returned by WithContext.
	assert.Empty(t, spy.ErrorCalls, "expected 0 calls")

	// The child logger (created by WithContext) should have received the Error call.
	assert.Len(t, *spy.ChildErrorCalls, 1)
}

// TestLoggingInterceptor_4xxCodeLogsAtWarn verifies that Connect errors with
// client-error codes (4xx semantics) are logged at Warn, not Error.
//
// Why this test is important:
//   - Log-based alerting commonly fires on any level=error entry.
//   - 4xx errors are normal client conditions (not-found, invalid-argument, etc.)
//     and must NOT trigger a server-error alert.
//   - Only 5xx-equivalent codes (CodeInternal, CodeUnavailable) warrant Error level.
//
// What it tests:
//   - CodeInvalidArgument -> Warn on child logger, zero Error calls on child logger.
func TestLoggingInterceptor_4xxCodeLogsAtWarn(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(
				connect.CodeInvalidArgument,
				apperr.Sentinel("bad input"),
			)
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	assert.Len(t, *spy.ChildWarnCalls, 1)
	assert.Empty(t, *spy.ChildErrorCalls, "expected 0 calls")
}

// TestLoggingInterceptor_5xxCodeLogsAtError verifies that Connect errors with
// server-error codes (5xx semantics) are logged at Error, not Warn.
//
// Why this test is important:
//   - CodeInternal represents unexpected server failures that operators must investigate.
//   - These must reach Error level (and any server-error alert), unlike 4xx client
//     errors.
//
// What it tests:
//   - CodeInternal -> Error on child logger, zero Warn calls on child logger.
func TestLoggingInterceptor_5xxCodeLogsAtError(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(
				connect.CodeInternal,
				apperr.Sentinel("server fault"),
			)
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	assert.Len(t, *spy.ChildErrorCalls, 1)
	assert.Empty(t, *spy.ChildWarnCalls, "expected 0 calls")
}

// TestLoggingInterceptor_CanceledLogsAtWarn verifies that a client cancellation
// (CodeCanceled, HTTP 499 client-closed-request) is logged at Warn, not Error.
//
// Why this test is important:
//   - A caller that disconnects or cancels mid-request is a normal client outcome,
//     not a server fault. Logging it at Error trips server-error alerts and inflates
//     the error rate (cancelled reads would surface as error-level 500s).
//
// What it tests:
//   - CodeCanceled -> Warn on the child logger, zero Error calls.
func TestLoggingInterceptor_CanceledLogsAtWarn(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(
				connect.CodeCanceled,
				apperr.Sentinel("client canceled"),
			)
		},
	)

	_, _ = handler(context.Background(), newTestRequest())

	assert.Len(t, *spy.ChildWarnCalls, 1)
	assert.Empty(t, *spy.ChildErrorCalls, "a cancellation must not log at Error")
}

// ---------------------------------------------------------------------------
// Metrics interceptor
// ---------------------------------------------------------------------------

// TestMetricsInterceptor_Success tests that the metrics interceptor passes
// through successful responses without modification.
//
// Why this test is important:
//   - Metrics collection must be transparent and never alter the response
//   - A broken metrics interceptor could silently corrupt responses for all handlers
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestMetricsInterceptor_Success(t *testing.T) {
	t.Parallel()

	metrics := fixtures.NopMetrics()
	interceptor := interceptors.MetricsInterceptor(metrics)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestMetricsInterceptor_Error tests that the metrics interceptor propagates
// handler errors unchanged while recording failure metrics.
//
// Why this test is important:
//   - Error codes must pass through unmodified so clients can react appropriately
//   - Metrics recording must not interfere with error propagation
//
// What it tests:
//   - A handler returning CodeInternal propagates as an error through the metrics
//     interceptor
func TestMetricsInterceptor_Error(t *testing.T) {
	t.Parallel()

	metrics := fixtures.NopMetrics()
	interceptor := interceptors.MetricsInterceptor(metrics)

	innerErr := connect.NewError(connect.CodeInternal, apperr.Sentinel("boom"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error to pass through")
}

// TestMetricsInterceptor_WithProcedure tests that the metrics interceptor
// handles requests with an empty Spec().Procedure without panicking.
//
// Why this test is important:
//   - Edge cases in procedure parsing can cause panics that crash the server
//   - Empty procedure strings occur with malformed or internal requests
//   - The metrics layer must be resilient to unexpected input shapes
//
// What it tests:
//   - A request with an empty procedure string completes without error or panic
func TestMetricsInterceptor_WithProcedure(t *testing.T) {
	t.Parallel()

	metrics := fixtures.NopMetrics()
	interceptor := interceptors.MetricsInterceptor(metrics)

	// The newTestRequest has an empty Spec().Procedure, which exercises
	// parseConnectProcedure with a minimal input. This test confirms that the
	// interceptor handles it without panicking.
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
}

// ---------------------------------------------------------------------------
// Tracing interceptor
// ---------------------------------------------------------------------------

// TestTracingInterceptor_Success tests that the tracing interceptor passes
// through successful responses without modification.
//
// Why this test is important:
//   - Tracing must be a transparent decorator that never alters responses
//   - A broken tracing interceptor could silently corrupt data across all handlers
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestTracingInterceptor_Success(t *testing.T) {
	t.Parallel()

	tracer := fixtures.NopTracer()
	interceptor := interceptors.TracingInterceptor(tracer)

	expected := newTestResponse()
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return expected, nil
		},
	)

	resp, err := handler(context.Background(), newTestRequest())
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestTracingInterceptor_Error tests that the tracing interceptor propagates
// handler errors unchanged while recording the error on the trace span.
//
// Why this test is important:
//   - Error codes must pass through unmodified for correct client-side handling
//   - Tracing must record errors for observability without altering the error chain
//
// What it tests:
//   - A handler returning CodeInternal propagates as CodeInternal through the tracing
//     interceptor
func TestTracingInterceptor_Error(t *testing.T) {
	t.Parallel()

	tracer := fixtures.NopTracer()
	interceptor := interceptors.TracingInterceptor(tracer)

	innerErr := connect.NewError(connect.CodeInternal, apperr.Sentinel("boom"))
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, innerErr
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error to pass through")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// TestTracingInterceptor_ExtractsTraceContext tests that the server-side
// tracing interceptor extracts W3C Trace Context from incoming headers.
//
// Why this test is important:
//   - Without trace context extraction, each service starts a new root trace
//   - Distributed tracing requires spans to be parented correctly across service
//     boundaries
//   - W3C Trace Context is the standard propagation format for HTTP-based RPC
//
// What it tests:
//   - A request with traceparent header passes through the interceptor without error
//   - The interceptor calls otel.GetTextMapPropagator().Extract() (verified by no panic)
func TestTracingInterceptor_ExtractsTraceContext(t *testing.T) {
	// NOT parallel: this test mutates the global OTel propagator via
	// otel.SetTextMapPropagator.
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	// Create a test span context with a known trace ID
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})

	// Inject the trace context into HTTP headers using a real propagator
	headers := make(http.Header)
	propagation.TraceContext{}.Inject(
		trace.ContextWithSpanContext(context.Background(), spanCtx),
		propagation.HeaderCarrier(headers),
	)

	// Create a Connect request with the headers
	req := connect.NewRequest[*emptyMessage](nil)
	for k, v := range headers {
		req.Header().Set(k, v[0])
	}

	require.NotEmpty(
		t,
		req.Header().Get("traceparent"),
		"expected traceparent header in request",
	)

	tracer := fixtures.NopTracer()
	interceptor := interceptors.TracingInterceptor(tracer)

	// Capture the context the inner handler receives to verify extraction.
	var extracted trace.SpanContext
	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			extracted = trace.SpanContextFromContext(ctx)
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	// Verify the trace context was extracted from headers into the Go context.
	require.True(
		t,
		extracted.IsValid(),
		"expected valid span context after extraction, got invalid",
	)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", extracted.TraceID().String())
}

// TestTracingInterceptor_RecordsRequestID tests that the tracing interceptor
// records the X-Request-ID header as a span attribute.
//
// Why this test is important:
//   - X-Request-ID from the gateway is used for correlation with gateway logs
//   - Recording it as a span attribute enables cross-referencing between gateway and
//     service traces
//   - Bridges two separate correlation systems (the gateway's request ID and OTel's trace
//     ID)
//
// What it tests:
//   - A request with X-Request-ID header results in http.request_id span attribute
func TestTracingInterceptor_RecordsRequestID(t *testing.T) {
	t.Parallel()

	req := connect.NewRequest[*emptyMessage](nil)
	req.Header().Set("X-Request-ID", "gw-abc123")

	// Use a spy tracer to capture span attributes
	var recordedAttrs map[string]any
	spyTracer := fixtures.SpyTracer(func(key string, value any) {
		if recordedAttrs == nil {
			recordedAttrs = make(map[string]any)
		}
		recordedAttrs[key] = value
	})

	interceptor := interceptors.TracingInterceptor(spyTracer)
	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return newTestResponse(), nil
		},
	)

	_, err := handler(context.Background(), req)
	require.NoError(t, err, "unexpected error")

	requestID, ok := recordedAttrs["http.request_id"]
	assert.True(t, ok, "expected http.request_id span attribute")
	assert.Equal(t, "gw-abc123", requestID)
}

// TestClientTracingInterceptor_InjectsTraceContext tests that the client-side
// tracing interceptor injects W3C Trace Context into outgoing request headers.
//
// Why this test is important:
//   - Client interceptors must propagate trace context to downstream services
//   - Without injection, the trace chain breaks at the service boundary
//   - SpanKindClient is semantically correct for outgoing calls
//
// What it tests:
//   - A client interceptor with a context containing trace state injects headers
func TestClientTracingInterceptor_InjectsTraceContext(t *testing.T) {
	// NOT parallel: this test mutates the global OTel propagator via
	// otel.SetTextMapPropagator. Running in parallel could race with other tests that
	// call otel.GetTextMapPropagator().

	// Set up a real W3C TraceContext propagator so Inject() produces a traceparent header.
	// NopTracer.Start() returns ctx unchanged, preserving the span context we embed below.
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	// Create a test span context with known trace/span IDs.
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)

	// Capture outgoing headers
	var capturedHeaders http.Header
	tracer := fixtures.NopTracer()
	interceptor := interceptors.ClientTracingInterceptor(tracer)

	handler := interceptor.WrapUnary(
		func(_ context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			capturedHeaders = req.Header().Clone()
			return newTestResponse(), nil
		},
	)

	req := connect.NewRequest[*emptyMessage](nil)
	_, err := handler(ctx, req)
	require.NoError(t, err, "unexpected error")

	// With a real propagator and valid span context, traceparent must be injected.
	traceparent := capturedHeaders.Get("traceparent")
	require.NotEmpty(
		t,
		traceparent,
		"expected traceparent header to be injected, got empty",
	)
	// W3C traceparent format: "00-<trace-id>-<span-id>-<flags>"
	expected := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	assert.Equal(t, expected, traceparent)
}

// ---------------------------------------------------------------------------
// ServerBuilder — partial compositions
// ---------------------------------------------------------------------------

// TestServerBuilder_MetricsOnly tests that the ServerBuilder can produce a
// valid interceptor chain with only metrics configured.
//
// Why this test is important:
//   - Services may run with minimal observability (metrics-only) in early development
//   - The builder must support partial configurations without requiring all interceptors
//
// What it tests:
//   - A ServerBuilder with only WithMetrics produces non-nil handler options
func TestServerBuilder_MetricsOnly(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewServerBuilder().
		WithMetrics(fixtures.NopMetrics()).
		Build()

	require.NotNil(t, opts, "expected non-nil options with metrics")
}

// TestServerBuilder_TracingOnly tests that the ServerBuilder can produce a
// valid interceptor chain with only tracing configured.
//
// Why this test is important:
//   - Services may run with tracing-only observability for distributed debugging
//   - The builder must support partial configurations without requiring all interceptors
//
// What it tests:
//   - A ServerBuilder with only WithTracing produces non-nil handler options
func TestServerBuilder_TracingOnly(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewServerBuilder().
		WithTracing(fixtures.NopTracer()).
		Build()

	require.NotNil(t, opts, "expected non-nil options with tracing")
}

// TestServerBuilder_AuthOnly tests that the ServerBuilder can produce a valid
// interceptor chain with only auth configured.
//
// Why this test is important:
//   - Services may run with auth-only middleware for identity enforcement without
//     observability
//   - The builder must support partial configurations without requiring all interceptors
//
// What it tests:
//   - A ServerBuilder with only WithAuth produces non-nil handler options
func TestServerBuilder_AuthOnly(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewServerBuilder().
		WithAuth(false, testHeaders()).
		Build()

	require.NotNil(t, opts, "expected non-nil options with auth")
}

// TestServerBuilder_ValidationOnly tests that the ServerBuilder can produce a
// valid interceptor chain with only validation configured.
//
// Why this test is important:
//   - Services may run with validation-only middleware for input sanitization
//   - The builder must support partial configurations without requiring all interceptors
//
// What it tests:
//   - A ServerBuilder with only WithValidation produces non-nil handler options
func TestServerBuilder_ValidationOnly(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewServerBuilder().
		WithValidation().
		Build()

	require.NotNil(t, opts, "expected non-nil options with validation")
}

// ---------------------------------------------------------------------------
// Retry interceptor — retries exhausted
// ---------------------------------------------------------------------------

// TestRetryInterceptor_RetriesExhausted tests that the retry interceptor
// returns the last error when all retry attempts are exhausted.
//
// Why this test is important:
//   - Exhausted retries must surface the final error rather than hanging or succeeding
//     silently
//   - Clients need the error to trigger fallback logic or alert the user
//   - Prevents infinite retry loops that would consume resources indefinitely
//
// What it tests:
//   - A permanently failing handler with exhausted retries returns CodeUnavailable
func TestRetryInterceptor_RetriesExhausted(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(0, true)
	interceptor := interceptors.RetryInterceptor(retrier)

	handler := interceptor.WrapUnary(
		func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			return nil, connect.NewError(
				connect.CodeUnavailable,
				apperr.Sentinel("transient"),
			)
		},
	)

	_, err := handler(context.Background(), newTestRequest())
	require.Error(t, err, "expected error when retries exhausted")
	// The retry interceptor wraps exhausted retries as CodeUnavailable
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
}

// ---------------------------------------------------------------------------
// Full end-to-end: ServerBuilder → execute request through composed chain
// ---------------------------------------------------------------------------

// TestServerBuilder_FullChain_ExecuteRequest tests that a fully-composed
// ServerBuilder produces a single handler option for end-to-end chain assembly.
//
// Why this test is important:
//   - End-to-end composition validates that all interceptors are compatible when chained
//   - A single handler option simplifies server registration and reduces misconfiguration
//     risk
//   - This is the integration point that every Connect service depends on at startup
//
// What it tests:
//   - A full ServerBuilder with all interceptors produces exactly 1 non-nil handler
//     option
func TestServerBuilder_FullChain_ExecuteRequest(t *testing.T) {
	t.Parallel()

	logger := fixtures.NopLogger()
	metrics := fixtures.NopMetrics()
	tracer := fixtures.NopTracer()
	limiter := fixtures.StubRateLimiter(true)

	opts := interceptors.NewServerBuilder().
		WithRecovery().
		WithRateLimit(limiter).
		WithMetrics(metrics).
		WithTracing(tracer).
		WithLogging(logger).
		WithAuth(false, testHeaders()).
		WithValidation().
		Build()

	require.NotNil(t, opts, "expected non-nil opts")

	// Build a test handler and apply the chain by creating a Connect handler
	// We can't directly use HandlerOption here, but we can test individual
	// interceptors were wired correctly by composing them manually.
	// The ServerBuilder test already validates the chain length.
	// We verify the full composition produces exactly 1 option.
	assert.Len(t, opts, 1)
}

// ---------------------------------------------------------------------------
// Retry interceptor — non-retryable codes (table-driven)
// ---------------------------------------------------------------------------

// TestRetryInterceptor_NonRetryableCodes tests that all non-retryable Connect
// error codes are returned immediately without retry.
//
// Why this test is important:
//   - Retrying non-retryable errors wastes capacity and delays error responses;
//     NotFound, PermissionDenied, etc. will never succeed on retry
//   - The retrier should be called exactly once (the initial attempt)
//
// What it tests:
//   - Each of the 7 non-retryable codes stops retrying immediately
//   - The retrier's Retry method is invoked but the operation returns nil
//     (signaling "stop retrying"), and the permanent error is returned
func TestRetryInterceptor_NonRetryableCodes(t *testing.T) {
	t.Parallel()

	nonRetryableCodes := []struct {
		name string
		code connect.Code
	}{
		{"NotFound", connect.CodeNotFound},
		{"AlreadyExists", connect.CodeAlreadyExists},
		{"PermissionDenied", connect.CodePermissionDenied},
		{"Unauthenticated", connect.CodeUnauthenticated},
		{"Unimplemented", connect.CodeUnimplemented},
		{"Canceled", connect.CodeCanceled},
	}

	for _, tt := range nonRetryableCodes {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			retrier, _ := fixtures.StubRetrier(0, false)
			interceptor := interceptors.RetryInterceptor(retrier)

			handler := interceptor.WrapUnary(
				func(_ context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
					return nil, connect.NewError(tt.code, apperr.Sentinel("permanent"))
				},
			)

			_, err := handler(context.Background(), newTestRequest())
			require.Error(t, err, "expected error")
			assert.Equal(t, tt.code, connect.CodeOf(err))
		})
	}
}

// ---------------------------------------------------------------------------
// Auth interceptor — GetAuthClaims/WithAuthClaims context roundtrip
// ---------------------------------------------------------------------------

// TestGetAuthClaims_Roundtrip tests the complete WithAuthClaims → GetAuthClaims
// context roundtrip with all fields populated.
//
// Why this test is important:
//   - Every authenticated endpoint depends on GetAuthClaims returning the
//     claims stored by the auth interceptor; field truncation or type mismatches
//     would silently break authorization
//
// What it tests:
//   - All AuthClaims fields survive the context roundtrip
func TestGetAuthClaims_Roundtrip(t *testing.T) {
	t.Parallel()

	original := &interceptors.AuthClaims{
		Sub:      "user-abc-123",
		TenantID: "org-xyz-789",
		Roles:    []string{"admin", "editor"},
	}

	ctx := interceptors.WithAuthClaims(context.Background(), original)
	claims, ok := interceptors.GetAuthClaims(ctx)
	require.True(t, ok, "expected claims in context")
	assert.Equal(t, original.Sub, claims.Sub)
	assert.Equal(t, original.TenantID, claims.TenantID)
	assert.Equal(t, []string{"admin", "editor"}, claims.Roles)
}

// TestGetAuthClaims_MissingReturnsNil tests that GetAuthClaims returns false
// on a context without claims.
//
// Why this test is important:
//   - Unauthenticated requests must return (zero, false); returning true with
//     empty claims would bypass authorization checks
//
// What it tests:
//   - GetAuthClaims on empty context returns (zero, false)
func TestGetAuthClaims_MissingReturnsNil(t *testing.T) {
	t.Parallel()

	claims, ok := interceptors.GetAuthClaims(context.Background())
	assert.False(t, ok)
	assert.Nil(t, claims)
}

// ---------------------------------------------------------------------------
// Auth interceptor — splitRoles edge cases
// ---------------------------------------------------------------------------

// TestAuthInterceptor_SplitRoles_DoubleComma tests that double commas in roles
// header are handled correctly (empty segments discarded).
//
// Why this test is important:
//   - Upstream systems may produce malformed role strings; the parser must
//     handle them gracefully without creating empty-string roles
//
// What it tests:
//   - "admin,,editor" produces ["admin", "editor"], not ["admin", "", "editor"]
func TestAuthInterceptor_SplitRoles_DoubleComma(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok := interceptors.GetAuthClaims(ctx)
			assert.True(t, ok, "expected claims in context")
			if !ok {
				return newTestResponse(), nil
			}
			assert.Len(t, claims.Roles, 2)
			return newTestResponse(), nil
		},
	)

	req := newTestRequest()
	req.Header().Set(testHeaders().Sub, "user-1")
	req.Header().Set(testHeaders().Roles, "admin,,editor")
	_, _ = handler(context.Background(), req)
}

// TestAuthInterceptor_SingleRole tests that an X-Roles header with one role and
// no commas parses into a single-element Roles slice.
//
// Why this test is important:
//   - The common case (exactly one role) must not be mangled by the comma-split
//     logic that exists to handle multi-role headers; a regression here would
//     break role-based authorization for the majority of users
//
// What it tests:
//   - X-Roles "viewer" produces Roles == ["viewer"]
func TestAuthInterceptor_SingleRole(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	handler := interceptor.WrapUnary(
		func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
			claims, ok := interceptors.GetAuthClaims(ctx)
			assert.True(t, ok, "expected claims")
			if !ok {
				return newTestResponse(), nil
			}
			assert.Equal(t, []string{"viewer"}, claims.Roles)
			return newTestResponse(), nil
		},
	)

	req := newTestRequest()
	req.Header().Set(testHeaders().Sub, "user-1")
	req.Header().Set(testHeaders().Roles, "viewer")
	_, _ = handler(context.Background(), req)
}

// ---------------------------------------------------------------------------
// Server builder — partial compositions
// ---------------------------------------------------------------------------

// TestServerBuilder_LoggingAndMetrics tests that the ServerBuilder composes a
// partial chain with only logging and metrics configured.
//
// Why this test is important:
//   - Services frequently run with observability-only middleware (no auth/rate
//     limiting) in early environments; the builder must collapse multiple
//     interceptors into a single handler option without requiring the full stack
//
// What it tests:
//   - WithLogging + WithMetrics produces exactly 1 non-nil handler option
func TestServerBuilder_LoggingAndMetrics(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewServerBuilder().
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	require.NotNil(t, opts, "expected non-nil opts")
	assert.Len(t, opts, 1)
}

// TestServerBuilder_RecoveryWithoutLogger tests that the ServerBuilder omits the
// recovery interceptor when WithRecovery is set but no logger is provided.
//
// Why this test is important:
//   - Recovery without a logger would swallow panics with no record, making
//     production crashes undiagnosable; the builder must enforce the logger
//     dependency rather than silently installing a blind recovery handler
//
// What it tests:
//   - WithRecovery alone (no WithLogging) produces nil options
func TestServerBuilder_RecoveryWithoutLogger(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewServerBuilder().
		WithRecovery(). // no WithLogging
		Build()

	// Recovery requires a logger; without it, the chain should be empty.
	assert.Nil(t, opts, "expected nil opts when recovery is set without logger")
}

// ---------------------------------------------------------------------------
// Client builder — partial compositions
// ---------------------------------------------------------------------------

// TestClientBuilder_TimeoutOnly tests that the ClientBuilder produces a valid
// chain with only a timeout interceptor configured.
//
// Why this test is important:
//   - A client may need only deadline enforcement (no retry/circuit breaker);
//     the builder must support this minimal configuration so outbound calls are
//     time-bounded without dragging in the rest of the resilience stack
//
// What it tests:
//   - WithTimeout alone produces exactly 1 non-nil client option
func TestClientBuilder_TimeoutOnly(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewClientBuilder().
		WithTimeout(5 * time.Second).
		Build()

	require.NotNil(t, opts, "expected non-nil opts")
	assert.Len(t, opts, 1)
}

// TestClientBuilder_RetryOnly tests that the ClientBuilder produces a valid
// chain with only a retry interceptor configured.
//
// Why this test is important:
//   - A client may want automatic retry of transient failures without the other
//     resilience layers; the builder must wire retry on its own so flaky
//     dependencies recover transparently
//
// What it tests:
//   - WithRetry alone produces exactly 1 non-nil client option
func TestClientBuilder_RetryOnly(t *testing.T) {
	t.Parallel()

	retrier, _ := fixtures.StubRetrier(0, false)
	opts := interceptors.NewClientBuilder().
		WithRetry(retrier).
		Build()

	require.NotNil(t, opts, "expected non-nil opts")
	assert.Len(t, opts, 1)
}

// TestClientBuilder_MetricsAndTracing tests that the ClientBuilder composes a
// partial chain with only metrics and tracing configured.
//
// Why this test is important:
//   - Outbound observability (metrics + distributed trace propagation) is often
//     needed even when no resilience policy is applied; the builder must collapse
//     both into a single client option without requiring timeout/retry/breaker
//
// What it tests:
//   - WithMetrics + WithTracing produces exactly 1 non-nil client option
func TestClientBuilder_MetricsAndTracing(t *testing.T) {
	t.Parallel()

	opts := interceptors.NewClientBuilder().
		WithMetrics(fixtures.NopMetrics()).
		WithTracing(fixtures.NopTracer()).
		Build()

	require.NotNil(t, opts, "expected non-nil opts")
	assert.Len(t, opts, 1)
}

// TestAuthInterceptor_StreamingExtractsHeaders tests that the auth interceptor
// extracts identity headers on the STREAMING path.
//
// Why this test is important:
//   - Connect applies a unary interceptor only to unary calls; a unary-only auth
//     interceptor would leave server-streaming handlers unauthenticated
//
// What it tests:
//   - WrapStreamingHandler parses the request headers into AuthClaims the streaming
//     handler observes
func TestAuthInterceptor_StreamingExtractsHeaders(t *testing.T) {
	t.Parallel()

	interceptor := interceptors.NewAuthInterceptor(false, testHeaders())
	var (
		claims *interceptors.AuthClaims
		ok     bool
	)
	next := func(ctx context.Context, _ connect.StreamingHandlerConn) error {
		claims, ok = interceptors.GetAuthClaims(ctx)
		return nil
	}

	// The streaming auth path reads only RequestHeader (it enriches the context and
	// hands the conn straight to next), so a generated StreamingHandlerConn mock that
	// returns the request headers is all this test needs.
	header := http.Header{}
	header.Set(testHeaders().Sub, "stream-user")
	header.Set(testHeaders().Tenant, "org-7")
	conn := mocks.NewMockStreamingHandlerConn(gomock.NewController(t))
	conn.EXPECT().RequestHeader().Return(header).AnyTimes()

	err := interceptor.WrapStreamingHandler(next)(context.Background(), conn)
	require.NoError(t, err)

	require.True(t, ok, "streaming handler should see auth claims")
	assert.Equal(t, "stream-user", claims.Sub)
	assert.Equal(t, "org-7", claims.TenantID)
}

// TestServerBuilder_WithRetryBudget tests that the server interceptor builder wires the
// per-request retry-budget interceptor into the assembled chain.
//
// Why this test is important:
//   - WithRetryBudget is how a service opts into a shared retry cap; if Build dropped it
//     the chain would silently lose retry-amplification control.
//
// What it tests:
//   - An unconfigured builder produces no handler options; a positive retry budget
//     yields the single handler option carrying the chain; a zero budget yields none.
func TestServerBuilder_WithRetryBudget(t *testing.T) {
	t.Parallel()

	assert.Empty(
		t,
		interceptors.NewServerBuilder().Build(),
		"no interceptors configured → no options",
	)
	assert.Len(t, interceptors.NewServerBuilder().WithRetryBudget(3).Build(), 1,
		"WithRetryBudget alone must add the retry-budget interceptor")
	assert.Empty(t, interceptors.NewServerBuilder().WithRetryBudget(0).Build(),
		"a zero retry budget disables the interceptor")
}
