package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorator"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// foundFullStack composes all five generic decorators around base in the canonical order
// (ARCHITECTURE.md#decorator-order; base → timeout → metrics → tracing → logging →
// recovery), the order the pipeline/workflow builders apply. onPanic is the tier's
// recovery contract.
func foundFullStack(
	base decorator.Executor[string, string],
	onPanic func(string, any) error,
) decorator.Executor[string, string] {
	e := base
	e = decorator.Timeout(e, time.Second)
	e = decorator.Metrics(
		e,
		"test",
		"svc",
		fixtures.NopMetrics(),
		decorator.DefaultBuckets(),
	)
	e = decorator.Tracing(e, fixtures.NopTracer(), "test", "svc")
	e = decorator.Logging(e, fixtures.NopLogger(), "test", "svc")
	e = decorator.Recovery(e, fixtures.NopLogger(), "test", "svc", onPanic)
	return e
}

// TestDecoratorUnwrapSeam tests that every generic decorator implements the
// Unwrapper seam, so a test can walk the composed stack down to the base handler.
//
// Why this test is important:
//   - The Unwrap seam is the sanctioned way a test
//     reaches the underlying handler through the decorator stack; without it, a
//     test would have to reach into unexported decorator internals. It also proves
//     the wrap depth/order: five decorators sit between the top and the base.
//
// What it tests:
//   - Walking Unwrap from the composed top makes exactly five hops and terminates
//     at the exact base instance.
func TestDecoratorUnwrapSeam(t *testing.T) {
	t.Parallel()
	base := fixtures.PassPipeline()
	top := foundFullStack(base, func(string, any) error { return nil })

	hops := 0
	cur := top
	for {
		u, ok := cur.(decorator.Unwrapper[string, string])
		if !ok {
			break
		}
		cur = u.Unwrap()
		hops++
	}

	require.Equal(t, 5, hops, "five decorators wrap the base")
	require.Same(
		t,
		base,
		cur.(*mocks.MockPipeline[string, string]),
		"Unwrap terminates at the exact base handler",
	)
}

// TestDecoratorRecoveryUsesOnPanic tests that the recovery decorator turns a panic
// into the tier-supplied error contract rather than a hardcoded one.
//
// Why this test is important:
//   - The pipeline tier returns a coded errors.Internal and the workflow tier a
//     plain wrap; the generic recovery must honor whichever onPanic the tier
//     supplies, or one tier's error contract would silently change.
//
// What it tests:
//   - A panicking base wrapped by Recovery returns exactly the onPanic error.
func TestDecoratorRecoveryUsesOnPanic(t *testing.T) {
	t.Parallel()
	sentinel := apperr.Sentinel("recovered by contract")
	base := fixtures.PanicPipeline()

	rec := decorator.Recovery[string, string](
		base, fixtures.NopLogger(), "test", "svc",
		func(string, any) error { return sentinel },
	)

	_, err := rec.Execute(context.Background(), "in")
	require.ErrorIs(t, err, sentinel)
}

// TestDecoratorStackPassesResultThrough tests that a happy-path execution returns
// the base's result unchanged through the full five-decorator stack.
//
// Why this test is important:
//   - The decorators are transparent on the success path; a decorator that dropped
//     or altered the result would corrupt every pipeline/workflow that composes it.
//
// What it tests:
//   - The full stack over a base returning "out-<in>" yields "out-x" with no error.
func TestDecoratorStackPassesResultThrough(t *testing.T) {
	t.Parallel()
	base := fixtures.StubPipeline(func(_ context.Context, in string) (string, error) {
		return "out-" + in, nil
	})
	top := foundFullStack(base, func(string, any) error { return nil })

	got, err := top.Execute(context.Background(), "x")
	require.NoError(t, err)
	require.Equal(t, "out-x", got)
}

// TestMetricsDecoratorLabelsByInstanceName tests that the metrics decorator labels
// every series by the per-instance name, not the constant tier.
//
// Why this test is important:
//   - The label VALUE is the only dimension that separates one pipeline's metrics
//     from another's. Passing the tier as the value would collapse every pipeline
//     into a single "pipeline"-labelled series, so "which pipeline is slow or
//     erroring" becomes unanswerable. A prior refactor regressed exactly this and
//     a no-op metrics mock could not catch it; this spy asserts the label value.
//
// What it tests:
//   - After one Execute through Metrics(tier="pipeline", name="create-item"),
//     every recorded Counter/Histogram label value is "create-item" (the
//     instance name), never "pipeline" (the tier).
func TestMetricsDecoratorLabelsByInstanceName(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)

	// The mock Counter/Histogram capture the label VALUES the decorator passes to
	// Inc/Observe into labels, so the test can assert each series is labelled by the
	// per-instance name rather than the constant tier. Both counters (executions +
	// errors) and the histogram share the one labels slice, mirroring the three
	// series the metrics decorator constructs.
	var labels []string
	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any()).Do(func(v ...string) {
		labels = append(labels, v...)
	}).AnyTimes()
	hist := mocks.NewMockHistogram(ctrl)
	hist.EXPECT().Observe(gomock.Any(), gomock.Any()).Do(func(_ float64, v ...string) {
		labels = append(labels, v...)
	}).AnyTimes()
	metricsMock := mocks.NewMockMetrics(ctrl)
	metricsMock.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).
		AnyTimes()
	metricsMock.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).
		AnyTimes()

	var inner decorator.Executor[string, string] = fixtures.StubPipeline(
		func(_ context.Context, in string) (string, error) { return in, nil },
	)
	m := decorator.Metrics(
		inner,
		"pipeline", "create-item",
		metricsMock,
		decorator.DefaultBuckets(),
	)

	_, err := m.Execute(context.Background(), "x")
	require.NoError(t, err)

	require.NotEmpty(t, labels, "the metrics decorator recorded at least one series")
	for _, v := range labels {
		require.Equal(
			t,
			"create-item",
			v,
			"series are labelled by the instance name, not the tier",
		)
	}
}

// TestDecoratorCircuitBreakerCodesOnlyRejections tests the generic breaker
// decorator.
//
// Why this test is important:
//   - A tier must tell "the breaker shed the call" (retry later) apart from "the
//     call ran and failed" (keep its own code); mixing them hides real errors
//
// What it tests:
//   - with the breaker rejecting without running inner, Execute returns the
//     onOpen error and inner is never called
//   - with the breaker running inner, inner's output and error pass through
//     unchanged ("in-out", nil)
func TestDecoratorCircuitBreakerCodesOnlyRejections(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	inner := mocks.NewMockPipeline[string, string](ctrl)
	onOpen := func(err error) error {
		return apperr.Wrap(err, apperr.CodeUnavailable, "shed")
	}

	open := mocks.NewMockCircuitBreaker(ctrl)
	open.EXPECT().Execute(gomock.Any()).Return(apperr.Sentinel("open"))
	_, err := decorator.CircuitBreaker[string, string](
		inner,
		open,
		onOpen,
	).Execute(context.Background(), "in")
	require.Equal(t, apperr.CodeUnavailable, apperr.Code(err))

	closed := mocks.NewMockCircuitBreaker(ctrl)
	closed.EXPECT().
		Execute(gomock.Any()).
		DoAndReturn(func(fn func() error) error { return fn() })
	inner.EXPECT().Execute(gomock.Any(), "in").Return("in-out", nil)
	out, err := decorator.CircuitBreaker[string, string](
		inner,
		closed,
		onOpen,
	).Execute(context.Background(), "in")
	require.NoError(t, err)
	require.Equal(t, "in-out", out)
}
