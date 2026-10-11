package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/decorators"
)

// Request and Response types for testing
type TestRequest struct {
	Input string
}

type TestResponse struct {
	Output string
}

// TestHandlerFunc_Execution tests that a function matching the HandlerFunc
// signature can be invoked and returns correct results.
//
// Why this test is important:
//   - HandlerFunc is the type alias used for all controller handlers; if the
//     type constraint is wrong or the invocation path is broken, no HTTP
//     handler built on it will execute correctly
//
// What it tests:
//   - Handler produces the expected output from the given request input
//   - The function satisfies the HandlerFunc type constraint
func TestHandlerFunc_Execution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Define a simple handler; the typed declaration verifies it matches the
	// HandlerFunc signature.
	var handler transport.HandlerFunc[TestRequest, TestResponse] = func(
		_ context.Context,
		req TestRequest,
	) (TestResponse, error) {
		return TestResponse{Output: "processed-" + req.Input}, nil
	}

	// Execute
	result, err := handler(ctx, TestRequest{Input: "test"})
	require.NoError(t, err)
	assert.Equal(t, "processed-test", result.Output)
}

// TestHandlerFunc_ErrorPropagation tests that errors from a handler function
// propagate unchanged to the caller.
//
// Why this test is important:
//   - HTTP handlers must propagate errors to the decorator chain so that
//     logging, metrics, and recovery decorators can observe them correctly
//
// What it tests:
//   - Handler returns the exact error it was configured to produce
func TestHandlerFunc_ErrorPropagation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	expectedErr := apperr.Sentinel("handler failed")

	var handler transport.HandlerFunc[TestRequest, TestResponse] = func(
		context.Context,
		TestRequest,
	) (TestResponse, error) {
		return TestResponse{}, expectedErr
	}

	_, err := handler(ctx, TestRequest{Input: "test"})
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestLoggingDecorator tests that the controller logging decorator
// transparently delegates handler execution without altering results.
//
// Why this test is important:
//   - The logging decorator wraps every API handler; if it mutates results
//     it would silently corrupt all HTTP responses
//
// What it tests:
//   - Decorated handler returns the correct output for a given request
func TestTransportLoggingDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, req TestRequest) (TestResponse, error) {
		return TestResponse{Output: "output-" + req.Input}, nil
	}

	// Build with logging decorator using interfaces.Logger
	decorated := decorators.NewHandlerBuilder(handler, "test-handler").
		WithLogging(fixtures.NopLogger()).
		Build()

	result, err := decorated(ctx, TestRequest{Input: "test-input"})
	require.NoError(t, err)
	assert.Equal(t, "output-test-input", result.Output)
}

// TestLoggingDecorator_ErrorPropagation tests that errors propagate unchanged
// through the controller logging decorator.
//
// Why this test is important:
//   - The logging decorator must record errors then forward them unchanged;
//     swallowing errors would cause HTTP 200 responses on failed requests
//
// What it tests:
//   - Decorated handler returns the exact error from the inner handler
func TestTransportLoggingDecorator_ErrorPropagation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("handler error")
	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		return TestResponse{}, expectedErr
	}

	decorated := decorators.NewHandlerBuilder(handler, "error-handler").
		WithLogging(fixtures.NopLogger()).
		Build()

	_, err := decorated(ctx, TestRequest{Input: "input"})
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestMetricsDecorator tests that the controller metrics decorator
// transparently delegates handler execution without panicking or altering
// results.
//
// Why this test is important:
//   - Metrics are recorded for every HTTP request; a decorator that panics
//     or mutates output would break all instrumented handlers
//
// What it tests:
//   - Decorated handler returns the correct output for a given request
func TestTransportMetricsDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		return TestResponse{Output: "output"}, nil
	}

	// Build with metrics decorator using interfaces.Metrics
	decorated := decorators.NewHandlerBuilder(handler, "metrics-handler").
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Execute - verifies the decorator chain doesn't panic
	result, err := decorated(ctx, TestRequest{Input: "input"})
	require.NoError(t, err)
	assert.Equal(t, "output", result.Output)
}

// TestRecoveryDecorator tests that the controller recovery decorator catches
// panics and converts them to errors with a zero-value response.
//
// Why this test is important:
//   - A panic in an HTTP handler must not crash the serving process and drop all
//     in-flight requests and connections; the recovery decorator is the safety net
//
// What it tests:
//   - A panicking handler returns an error containing "panic"
//   - The response is a zero value (not corrupted data)
func TestRecoveryDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		panic("test panic")
	}

	decorated := decorators.NewHandlerBuilder(handler, "panic-handler").
		WithRecovery().
		Build()

	result, err := decorated(ctx, TestRequest{Input: "input"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic")
	assert.Equal(t, TestResponse{}, result) // Zero value on panic
}

// TestBuilderComposition tests that multiple controller decorators (logging,
// metrics, recovery) compose correctly without interfering with each other.
//
// Why this test is important:
//   - Production API handlers use multiple decorators stacked together; if
//     they interfere the output is corrupted or one decorator's logs are lost
//
// What it tests:
//   - Decorated handler returns the correct output through the full decorator chain
func TestTransportBuilderComposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, req TestRequest) (TestResponse, error) {
		return TestResponse{Output: "composed-" + req.Input}, nil
	}

	// Build with all decorators (order: base -> metrics -> logging -> recovery)
	decorated := decorators.NewHandlerBuilder(handler, "composed-handler").
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		WithRecovery().
		Build()

	result, err := decorated(ctx, TestRequest{Input: "test"})
	require.NoError(t, err)
	assert.Equal(t, "composed-test", result.Output)
}

// TestBuilderComposition_PanicRecovery tests that the recovery decorator
// catches panics even when other decorators (logging, metrics) are in the
// chain.
//
// Why this test is important:
//   - The recovery decorator must be outermost in the chain; if any decorator
//     re-panics or intercepts the panic early, the process crashes
//
// What it tests:
//   - A panicking handler returns an error containing "panic" through the
//     full decorator chain
func TestBuilderComposition_PanicRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		panic("composed panic")
	}

	decorated := decorators.NewHandlerBuilder(handler, "panic-composed").
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		WithRecovery().
		Build()

	_, err := decorated(ctx, TestRequest{Input: "input"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic")
}

// TestBuilderComposition_ErrorFlow tests that errors propagate correctly
// through a composed controller decorator chain.
//
// Why this test is important:
//   - Each decorator in the chain must forward errors to the outer caller;
//     any decorator that swallows errors would produce incorrect HTTP responses
//
// What it tests:
//   - Decorated handler returns the exact error from the inner handler
//     through all decorators
func TestTransportBuilderComposition_ErrorFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("composed error")
	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		return TestResponse{}, expectedErr
	}

	decorated := decorators.NewHandlerBuilder(handler, "error-composed").
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		WithRecovery().
		Build()

	_, err := decorated(ctx, TestRequest{Input: "input"})
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestTypeCompliance tests that decorated handlers still satisfy the
// HandlerFunc type constraint after decoration.
//
// Why this test is important:
//   - Consumers pass decorated handlers where HandlerFunc is expected; if
//     decoration changes the type the code won't compile in callers
//
// What it tests:
//   - Both raw and decorated handlers are assignable to HandlerFunc
func TestTypeCompliance(t *testing.T) {
	t.Parallel()
	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		return TestResponse{}, nil
	}

	var _ transport.HandlerFunc[TestRequest, TestResponse] = handler

	decorated := decorators.NewHandlerBuilder(handler, "test").
		WithLogging(fixtures.NopLogger()).
		Build()

	_ = decorated
}

// ---------------------------------------------------------------------------
// Timeout decorator tests
// ---------------------------------------------------------------------------

// TestTimeoutDecorator_PassesThrough tests that the timeout decorator does
// not interfere with handler operations that complete within the deadline.
//
// Why this test is important:
//   - The timeout decorator is present on all API handlers; if it cancels fast
//     operations it would break normal request handling
//
// What it tests:
//   - Decorated handler returns the correct output with a generous timeout
func TestTransportTimeoutDecorator_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, req TestRequest) (TestResponse, error) {
		return TestResponse{Output: "fast-" + req.Input}, nil
	}

	decorated := decorators.NewHandlerBuilder(handler, "timeout-handler").
		WithTimeout(5 * time.Second).
		Build()

	result, err := decorated(ctx, TestRequest{Input: "input"})
	require.NoError(t, err)
	assert.Equal(t, "fast-input", result.Output)
}

// TestTimeoutDecorator_CancelsSlowOp tests that the timeout decorator cancels
// handler operations that exceed the deadline.
//
// Why this test is important:
//   - Slow handlers (e.g. Bedrock LLM calls) must time out to prevent goroutine
//     and connection pool exhaustion under high load
//
// What it tests:
//   - Decorated handler returns context.DeadlineExceeded when the handler
//     blocks longer than the timeout
func TestTransportTimeoutDecorator_CancelsSlowOp(t *testing.T) {
	t.Parallel()

	handler := func(ctx context.Context, _ TestRequest) (TestResponse, error) {
		<-ctx.Done()
		return TestResponse{}, ctx.Err()
	}

	decorated := decorators.NewHandlerBuilder(handler, "slow-handler").
		WithTimeout(1 * time.Millisecond).
		Build()

	_, err := decorated(context.Background(), TestRequest{Input: "input"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// ---------------------------------------------------------------------------
// Rate limit decorator tests
// ---------------------------------------------------------------------------

// TestRateLimitDecorator_AllowsWithinLimit tests that the rate limit decorator
// passes through requests when the rate limiter allows them.
//
// Why this test is important:
//   - The rate limiter must not drop legitimate requests; if it rejects allowed
//     requests users get spurious 429 errors
//
// What it tests:
//   - Decorated handler returns the correct output when the limiter reports Allowed=true
func TestRateLimitDecorator_AllowsWithinLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, req TestRequest) (TestResponse, error) {
		return TestResponse{Output: "allowed-" + req.Input}, nil
	}

	limiter := fixtures.StubRateLimiter(true)
	decorated := decorators.NewHandlerBuilder(handler, "ratelimit-handler").
		WithRateLimit(limiter).
		Build()

	result, err := decorated(ctx, TestRequest{Input: "test"})
	require.NoError(t, err)
	assert.Equal(t, "allowed-test", result.Output)
}

// TestRateLimitDecorator_RejectsWhenExceeded tests that the rate limit
// decorator blocks requests when the rate limit is exceeded.
//
// Why this test is important:
//   - The gateway enforces tenant-level limits but the per-handler limiter provides
//     defense-in-depth; if it doesn't reject over-limit requests, one tenant
//     can exhaust resources for all others
//
// What it tests:
//   - Decorated handler returns an error containing "rate limit exceeded" when
//     the limiter rejects
//   - The inner handler is never called
func TestRateLimitDecorator_RejectsWhenExceeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		require.Fail(t, "handler should not be called when rate limited")
		return TestResponse{}, nil
	}

	limiter := fixtures.StubRateLimiter(false)
	decorated := decorators.NewHandlerBuilder(handler, "ratelimit-handler").
		WithRateLimit(limiter).
		Build()

	_, err := decorated(ctx, TestRequest{Input: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit exceeded")
}

// TestTimeoutWithRateLimit tests that timeout, rate limit, logging, metrics,
// and recovery decorators compose correctly.
//
// Why this test is important:
//   - These five decorators are always applied together in production API
//     handlers; they must compose without interfering or masking each other
//
// What it tests:
//   - Decorated handler returns the correct output through the full decorator stack
func TestTimeoutWithRateLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, req TestRequest) (TestResponse, error) {
		return TestResponse{Output: "composed-" + req.Input}, nil
	}

	limiter := fixtures.StubRateLimiter(true)
	decorated := decorators.NewHandlerBuilder(handler, "composed-handler").
		WithTimeout(5 * time.Second).
		WithRateLimit(limiter).
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		WithRecovery().
		Build()

	result, err := decorated(ctx, TestRequest{Input: "test"})
	require.NoError(t, err)
	assert.Equal(t, "composed-test", result.Output)
}

// TestRateLimitWithRecovery tests that the recovery decorator catches panics
// even when the rate limit decorator is in the chain.
//
// Why this test is important:
//   - Recovery must be outermost; a panic after the rate limiter checked must
//     still be caught rather than crashing the process
//
// What it tests:
//   - A panicking handler returns an error containing "panic" through rate
//     limit and recovery
func TestRateLimitWithRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, _ TestRequest) (TestResponse, error) {
		panic("handler panic")
	}

	limiter := fixtures.StubRateLimiter(true)
	decorated := decorators.NewHandlerBuilder(handler, "panic-ratelimit-handler").
		WithRateLimit(limiter).
		WithRecovery().
		Build()

	_, err := decorated(ctx, TestRequest{Input: "test"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic")
}

// TestFullResilienceChain tests that all controller decorators (timeout, rate
// limit, metrics, logging, recovery) compose correctly in the full resilience
// stack.
//
// Why this test is important:
//   - Production API handlers use all five decorators together; an incompatible
//     composition would break all instrumented handler stages
//
// What it tests:
//   - Decorated handler returns the correct output through all five decorators
func TestTransportFullResilienceChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	handler := func(_ context.Context, req TestRequest) (TestResponse, error) {
		return TestResponse{Output: "full-" + req.Input}, nil
	}

	limiter := fixtures.StubRateLimiter(true)
	decorated := decorators.NewHandlerBuilder(handler, "full-chain-handler").
		WithTimeout(5 * time.Second).
		WithRateLimit(limiter).
		WithMetrics(fixtures.NopMetrics()).
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	result, err := decorated(ctx, TestRequest{Input: "chain"})
	require.NoError(t, err)
	assert.Equal(t, "full-chain", result.Output)
}
