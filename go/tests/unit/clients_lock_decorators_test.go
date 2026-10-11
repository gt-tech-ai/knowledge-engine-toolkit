package unit_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestLockDecorators_FullStackWrapOrder tests that the decorator builder engages every
// cross-cutting concern and nests them in the documented order
// (ARCHITECTURE.md#decorator-order).
//
// Why this test is important:
//   - The One Idea requires the lock's business logic to be wrapped by the full
//     resilience + observability stack. This asserts all six decorators are wired
//     AND that observability wraps resilience wraps the backend (Tracing
//     outermost, backend innermost), so a span/metric covers the whole resilient
//     operation and the circuit-breaker sits outside retry.
//
// What it tests:
//   - On Acquire, the entry sequence is trace → circuit-breaker → retry → backend
//     (each layer engaged, correctly nested) and the metrics + logging decorators
//     record.
func TestLockDecorators_FullStackWrapOrder(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	// order records each decorator's entry point so the composed nesting is
	// asserted purely from the call sequence.
	var order []string

	inner := mocks.NewMockDistributedLock(ctrl)
	inner.EXPECT().Acquire(gomock.Any(), "k").
		DoAndReturn(func(context.Context, string) (string, bool, error) {
			order = append(order, "inner")
			return "tok", true, nil
		})

	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order = append(order, "log") }).AnyTimes()
	logger.EXPECT().Error(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order = append(order, "log") }).AnyTimes()

	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any(), gomock.Any(), gomock.Any()).
		Do(func(...string) { order = append(order, "metric") }).AnyTimes()
	hist := mocks.NewMockHistogram(ctrl)
	hist.EXPECT().Observe(gomock.Any(), gomock.Any(), gomock.Any()).
		Do(func(float64, ...string) { order = append(order, "metric") }).AnyTimes()
	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).AnyTimes()
	metrics.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).AnyTimes()

	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().End().AnyTimes()
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	tracer := mocks.NewMockTracer(ctrl)
	tracer.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			ctx context.Context,
			_ string,
			_ ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			order = append(order, "trace")
			return ctx, span
		}).AnyTimes()

	retrier := mocks.NewMockRetrier(ctrl)
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, op func() error) error {
			order = append(order, "retry")
			return op()
		}).AnyTimes()
	cb := mocks.NewMockCircuitBreaker(ctrl)
	cb.EXPECT().Execute(gomock.Any()).
		DoAndReturn(func(op func() error) error {
			order = append(order, "cb")
			return op()
		}).AnyTimes()

	lk := decorators.NewBuilder(inner, "test").
		WithRetry(retrier).
		WithCircuitBreaker(cb).
		WithTimeout(time.Second).
		WithLogging(logger).
		WithMetrics(metrics).
		WithTracing(tracer).
		Build()

	tok, acquired, err := lk.Acquire(context.Background(), "k")
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Equal(t, "tok", tok)

	require.Equal(t, "trace", order[0], "tracing is the outermost decorator")
	require.Less(t, slices.Index(order, "trace"), slices.Index(order, "cb"),
		"observability wraps resilience")
	require.Less(t, slices.Index(order, "cb"), slices.Index(order, "retry"),
		"circuit-breaker wraps retry")
	require.Less(t, slices.Index(order, "retry"), slices.Index(order, "inner"),
		"resilience wraps the backend")
	require.Contains(t, order, "metric", "the metrics decorator must record")
	require.Contains(t, order, "log", "the logging decorator must record")
}

// TestLockDecorators_RenewAndRelease_FullStack tests that Renew and Release flow
// through the whole decorator stack to the backend, exercising the resilience +
// observability wrappers on the lease-renewal and release paths (not just Acquire).
//
// Why this test is important:
//   - The renewal watchdog (Renew) and the release path run under the same decorator
//     stack as Acquire; if a decorator dropped or mishandled Renew/Release, a lease
//     could silently stop renewing or a lock could leak — with no span, metric, or log,
//     and no retry/circuit-breaker protection on those round-trips.
//
// What it tests:
//   - Renew and Release each reach the backend through the full stack and return its
//     result (held=true / nil), with the observability decorators recording.
func TestLockDecorators_RenewAndRelease_FullStack(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	inner := mocks.NewMockDistributedLock(ctrl)
	inner.EXPECT().Renew(gomock.Any(), "k", "tok").Return(true, nil)
	inner.EXPECT().Release(gomock.Any(), "k", "tok").Return(nil)

	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).AnyTimes()
	logger.EXPECT().Error(gomock.Any(), gomock.Any()).AnyTimes()

	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	hist := mocks.NewMockHistogram(ctrl)
	hist.EXPECT().Observe(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).AnyTimes()
	metrics.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).AnyTimes()

	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().End().AnyTimes()
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	tracer := mocks.NewMockTracer(ctrl)
	tracer.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			ctx context.Context, _ string, _ ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			return ctx, span
		}).AnyTimes()

	retrier := mocks.NewMockRetrier(ctrl)
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, op func() error) error { return op() }).
		AnyTimes()
	cb := mocks.NewMockCircuitBreaker(ctrl)
	cb.EXPECT().Execute(gomock.Any()).
		DoAndReturn(func(op func() error) error { return op() }).AnyTimes()

	lk := decorators.NewBuilder(inner, "test").
		WithRetry(retrier).
		WithCircuitBreaker(cb).
		WithTimeout(time.Second).
		WithLogging(logger).
		WithMetrics(metrics).
		WithTracing(tracer).
		Build()

	held, err := lk.Renew(context.Background(), "k", "tok")
	require.NoError(t, err)
	assert.True(t, held, "Renew flows through the stack and returns the backend's held")

	require.NoError(t, lk.Release(context.Background(), "k", "tok"),
		"Release flows through the stack to the backend")
}

// TestLockDecorators_Renew_ErrorObserved tests that a backend Renew error flows back
// through the stack and is recorded on the observability decorators, not swallowed.
//
// Why this test is important:
//   - A failing lease renewal is the signal that the lock may be lost; the error must
//     surface to the caller AND be logged/traced so an operator can see renewals
//     failing before the lease is dropped.
//
// What it tests:
//   - When the backend Renew errors, the decorated Renew returns (false, err) and the
//     logging + tracing decorators record the failure.
func TestLockDecorators_Renew_ErrorObserved(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	inner := mocks.NewMockDistributedLock(ctrl)
	inner.EXPECT().Renew(gomock.Any(), "k", "tok").
		Return(false, apperr.New(apperr.CodeUnavailable, "renew blip"))

	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).AnyTimes()
	loggedErr := false
	logger.EXPECT().Error(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { loggedErr = true }).AnyTimes()

	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().End().AnyTimes()
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	recordedErr := false
	span.EXPECT().
		RecordError(gomock.Any()).
		Do(func(error) { recordedErr = true }).
		AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	tracer := mocks.NewMockTracer(ctrl)
	tracer.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			ctx context.Context, _ string, _ ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			return ctx, span
		}).AnyTimes()

	lk := decorators.NewBuilder(inner, "test").
		WithLogging(logger).
		WithTracing(tracer).
		Build()

	held, err := lk.Renew(context.Background(), "k", "tok")
	require.Error(t, err)
	assert.False(t, held, "a failed renew must not report the lease as held")
	assert.True(t, loggedErr, "the failure must be logged")
	assert.True(t, recordedErr, "the failure must be recorded on the span")
}

// TestLockDecorators_CircuitBreakerFailClosed tests that the circuit-breaker
// decorator surfaces the error without fabricating a result when the circuit is
// open.
//
// Why this test is important:
//   - For a lock, "graceful degradation" is unsafe in either direction: a fake
//     acquired=true admits a duplicate operation; a fake acquired=false wedges every
//     pod. The breaker must fail closed (surface the error) and NOT call the
//     backend, letting the caller decide how to degrade.
//
// What it tests:
//   - When Execute returns an error (open), Acquire returns acquired=false with the
//     error and never invokes the backend.
func TestLockDecorators_CircuitBreakerFailClosed(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	inner := mocks.NewMockDistributedLock(ctrl) // no EXPECT: must not be called
	cb := mocks.NewMockCircuitBreaker(ctrl)
	cb.EXPECT().Execute(gomock.Any()).
		Return(apperr.New(apperr.CodeUnavailable, "circuit open"))

	lk := decorators.NewBuilder(inner, "test").WithCircuitBreaker(cb).Build()

	tok, acquired, err := lk.Acquire(context.Background(), "k")
	require.Error(t, err)
	assert.False(t, acquired, "must not fabricate acquired when the breaker is open")
	assert.Empty(t, tok)
}

// loopRetrier returns a mock Retrier that mimics a real backoff: it retries the
// op up to 3 times, stopping on the first nil error.
func loopRetrier(ctrl *gomock.Controller) *mocks.MockRetrier {
	r := mocks.NewMockRetrier(ctrl)
	r.EXPECT().Retry(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, op func() error) error {
			var err error
			for range 3 {
				if err = op(); err == nil {
					return nil
				}
			}
			return err
		}).AnyTimes()
	return r
}

// TestLockDecorators_Retry_ContentionNotRetried tests that a contended Acquire
// (acquired=false, nil error) is not retried.
//
// Why this test is important:
//   - Acquire is a non-blocking try-lock: losing the race is a normal result, not
//     a failure. Retrying contention would turn a try-lock into a spin and
//     mis-map "someone else holds it" into wasted backend calls.
//
// What it tests:
//   - With a contending backend (acquired=false, nil err), the backend is called
//     exactly once even through the retry decorator.
func TestLockDecorators_Retry_ContentionNotRetried(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	inner := mocks.NewMockDistributedLock(ctrl)
	inner.EXPECT().Acquire(gomock.Any(), "k").Return("", false, nil).Times(1)

	lk := decorators.NewBuilder(inner, "test").WithRetry(loopRetrier(ctrl)).Build()

	_, acquired, err := lk.Acquire(context.Background(), "k")
	require.NoError(t, err)
	assert.False(t, acquired, "a contended acquire stays acquired=false")
}

// TestLockDecorators_Retry_TransientErrorRetried tests that a transient backend
// error is retried and a subsequent success is returned.
//
// Why this test is important:
//   - A blip on the Redis round-trip (not contention) should be retried so a
//     recoverable error doesn't spuriously fail an acquire.
//
// What it tests:
//   - A backend that errors once then succeeds yields acquired=true, with the
//     backend called twice.
func TestLockDecorators_Retry_TransientErrorRetried(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	inner := mocks.NewMockDistributedLock(ctrl)
	gomock.InOrder(
		inner.EXPECT().Acquire(gomock.Any(), "k").
			Return("", false, apperr.New(apperr.CodeUnavailable, "blip")),
		inner.EXPECT().Acquire(gomock.Any(), "k").Return("tok", true, nil),
	)

	lk := decorators.NewBuilder(inner, "test").WithRetry(loopRetrier(ctrl)).Build()

	tok, acquired, err := lk.Acquire(context.Background(), "k")
	require.NoError(t, err)
	assert.True(t, acquired, "the retry succeeds after a transient error")
	assert.Equal(t, "tok", tok)
}
