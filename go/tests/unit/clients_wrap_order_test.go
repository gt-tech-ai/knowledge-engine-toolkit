package unit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.uber.org/mock/gomock"

	decorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	jobdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/decorators"
	msgdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/deadletter"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// obsOrder records the observability call sequence ("trace" / "metric" / "log") in the
// order the client stack invokes each collaborator, so a test can assert the composed
// order (ARCHITECTURE.md#decorator-order). It is plain test data — it implements no
// production interface; the generated metrics/logger mocks and the orderTracer below
// append to it as the stack drives them.
type obsOrder struct {
	tags []string
	mu   sync.Mutex
}

func (o *obsOrder) add(tag string) {
	o.mu.Lock()
	o.tags = append(o.tags, tag)
	o.mu.Unlock()
}

func (o *obsOrder) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.tags...)
}

// orderTracer is the one irreducible test double in this package: go.uber.org/mock
// CANNOT generate a usable mock for go.opentelemetry.io/otel/trace.Tracer, because
// that interface embeds embedded.Tracer, whose marker method tracer() is unexported
// to package embedded — a mock generated in package mocks cannot satisfy it (a
// compile check confirms `*mocks.MockTracer does not implement trace.Tracer
// (unexported method tracer)`). So the OTel tracer seam stays hand-rolled; it embeds
// embedded.Tracer to satisfy that guard and records the span start into the shared
// order log, returning the context's no-op span. Every other collaborator in this
// test is a generated mock.
type orderTracer struct {
	embedded.Tracer
	order        *obsOrder
	markDeadline bool
}

// Start records the span start and returns the context's (no-op) span. When
// markDeadline is set it first records "timeout" if ctx already carries a deadline,
// which shows the Timeout layer sits outside Tracing.
func (t orderTracer) Start(
	ctx context.Context,
	_ string,
	_ ...oteltrace.SpanStartOption,
) (context.Context, oteltrace.Span) {
	if _, ok := ctx.Deadline(); ok && t.markDeadline {
		t.order.add("timeout")
	}
	t.order.add("trace")
	return ctx, oteltrace.SpanFromContext(ctx)
}

// orderedMetrics returns a generated Metrics mock whose client-stack instruments
// (ops/errors counters, duration histogram) record a "metric" tag on every use.
func orderedMetrics(ctrl *gomock.Controller, order *obsOrder) *mocks.MockMetrics {
	ops := mocks.NewMockCounter(ctrl)
	errs := mocks.NewMockCounter(ctrl)
	dur := mocks.NewMockHistogram(ctrl)
	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().
		Counter("client_operations_total", gomock.Any(), gomock.Any()).
		Return(ops).AnyTimes()
	metrics.EXPECT().
		Counter("client_errors_total", gomock.Any(), gomock.Any()).
		Return(errs).AnyTimes()
	metrics.EXPECT().
		Histogram(
			"client_operation_duration_seconds", gomock.Any(), gomock.Any(), gomock.Any(),
		).
		Return(dur).AnyTimes()
	ops.EXPECT().Inc(gomock.Any()).Do(func(...string) { order.add("metric") }).AnyTimes()
	errs.EXPECT().Inc(gomock.Any()).Do(func(...string) { order.add("metric") }).AnyTimes()
	dur.EXPECT().
		Observe(gomock.Any(), gomock.Any()).
		Do(func(float64, ...string) { order.add("metric") }).AnyTimes()
	return metrics
}

// orderedLogger returns a generated Logger mock that records a "log" tag on Debug
// (the inner-seam failure level) and, defensively, on Error.
func orderedLogger(ctrl *gomock.Controller, order *obsOrder) *mocks.MockLogger {
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().
		Debug(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order.add("log") }).AnyTimes()
	logger.EXPECT().
		Error(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order.add("log") }).AnyTimes()
	return logger
}

// collapseRuns drops consecutive duplicate tags, so a layer that records several
// times in a row (the three client-stack instruments) reads as one step.
func collapseRuns(tags []string) []string {
	var out []string
	for _, tag := range tags {
		if len(out) == 0 || out[len(out)-1] != tag {
			out = append(out, tag)
		}
	}
	return out
}

// hasDeadline reports whether ctx carries a deadline.
func hasDeadline(ctx context.Context) bool {
	_, ok := ctx.Deadline()
	return ok
}

// TestClientStack_WrapOrder_TracingMetricsLogging locks the observability
// order for the client stack: Tracing (outermost) → Metrics → Logging (innermost).
//
// Why this test is important:
//   - The order is semantic (ARCHITECTURE.md#decorator-order): the tracing span must
//     bracket the whole operation, and the failure log is the innermost seam. The
//     repo/service builders once had this inverted (Logging outermost); this test
//     prevents a regression back to that.
//
// What it tests:
//   - On a failing op the recorded observability sequence starts with the tracing span,
//     records metrics next, and logs last — every metric strictly before the log.
func TestClientStack_WrapOrder_TracingMetricsLogging(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	order := &obsOrder{}

	// Metrics: the stack records ops.Inc, dur.Observe, errs.Inc on a failing op, each
	// tagged "metric". Logger: the client stack is an INNER seam, so a failed op logs
	// at Debug, tagged "log".
	metrics := orderedMetrics(ctrl, order)
	logger := orderedLogger(ctrl, order)

	tracer := orderTracer{order: order}
	s := decorators.New("test").WithTracer(tracer).WithMetrics(metrics).WithLogger(logger)

	_, _ = decorators.Run(
		context.Background(),
		s,
		"op",
		decorators.RunOpts{},
		func(context.Context) (string, error) { return "", errBoom },
	) // fail so logging fires

	tags := order.snapshot()
	require.NotEmpty(t, tags, "the stack must record trace/metric/log tags")
	require.Equal(
		t,
		"trace",
		tags[0],
		"tracing span must start first (Tracing outermost)",
	)
	require.Equal(t, "log", tags[len(tags)-1], "logging must be last (Logging innermost)")

	logIdx := len(tags) - 1
	for i, tag := range tags {
		if tag == "metric" {
			require.Less(
				t,
				i,
				logIdx,
				"metrics must be recorded before the log (Metrics outside Logging)",
			)
		}
	}
}

// TestEventAndJobStacks_WrapOrder pins the full EventHandler and Job decorator
// orders (ARCHITECTURE.md#decorator-order) as they run, with every layer wired:
//   - EventHandler: Dedup → DeadLetter → Retry → CircuitBreaker → Timeout → Tracing →
//     Metrics → Logging → Handler.
//   - Job: LeaderElection → RateLimit → Retry → Timeout → Tracing → Metrics → Logging →
//     Job.
//
// Why this test is important:
//   - The order is semantic: dedup and leader election must short-circuit before any
//     work, retry must sit outside the breaker so every attempt is observed, the
//     per-attempt timeout must sit inside retry (a fresh budget per attempt) and outside
//     tracing, and a message is dead-lettered only after the inner stack has given up.
//     The EventHandler comment once described an order the code never had; without
//     this test a reorder of WrapHandler/WrapJob would pass the suite.
//
// What it tests:
//   - With a handler/job that fails, each collaborator's call is recorded in order:
//     the recorded sequence equals the documented order (inner layers first on the way
//     in, metrics then logging on the way out, dead-letter last).
//   - Retry receives a context with no deadline while the handler/job receives one,
//     so the timeout is applied per attempt inside retry.
func TestEventAndJobStacks_WrapOrder(t *testing.T) {
	t.Parallel()

	// retrierRecording records "retry" and whether its ctx had a deadline, then runs
	// the attempt once.
	retrierRecording := func(
		ctrl *gomock.Controller, order *obsOrder, retryCtxDeadline *bool,
	) *mocks.MockRetrier {
		retrier := mocks.NewMockRetrier(ctrl)
		retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, op func() error) error {
				order.add("retry")
				*retryCtxDeadline = hasDeadline(ctx)
				return op()
			})
		return retrier
	}

	t.Run("EventHandler", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		order := &obsOrder{}

		dedup := mocks.NewMockDeduplicator(ctrl)
		dedup.EXPECT().Seen(gomock.Any(), "m-1").DoAndReturn(
			func(context.Context, string) (bool, error) {
				order.add("dedup")
				return false, nil
			})
		cb := mocks.NewMockCircuitBreaker(ctrl)
		cb.EXPECT().Execute(gomock.Any()).DoAndReturn(func(fn func() error) error {
			order.add("cb")
			return fn()
		})
		dlq := mocks.NewMockDeadLetterBackend(ctrl)
		dlq.EXPECT().Send(gomock.Any(), gomock.Any()).DoAndReturn(
			func(context.Context, interfaces.DeadLetter) error {
				order.add("deadletter")
				return nil
			})
		var retryCtxDeadline, handlerCtxDeadline bool

		handler := msgdecorators.WrapHandler(
			func(ctx context.Context, _ *interfaces.Message) error {
				order.add("handler")
				handlerCtxDeadline = hasDeadline(ctx)
				return errBoom
			},
			msgdecorators.EventStackDeps{
				Dedup:          dedup,
				DeadLetter:     deadletter.New(dlq, nil),
				Retrier:        retrierRecording(ctrl, order, &retryCtxDeadline),
				CircuitBreaker: cb,
				Tracer:         orderTracer{order: order, markDeadline: true},
				Metrics:        orderedMetrics(ctrl, order),
				Logger:         orderedLogger(ctrl, order),
				Name:           "events",
				Timeout:        time.Minute,
			},
		)

		require.NoError(
			t,
			handler(context.Background(), &interfaces.Message{ID: "m-1"}),
			"a dead-lettered message is acked",
		)
		require.Equal(t, []string{
			"dedup",
			"retry",
			"cb",
			"timeout",
			"trace",
			"handler",
			"metric",
			"log",
			"deadletter",
		}, collapseRuns(order.snapshot()))
		require.False(t, retryCtxDeadline, "the timeout is applied inside retry")
		require.True(
			t,
			handlerCtxDeadline,
			"the handler runs under the per-attempt timeout",
		)
	})

	t.Run("Job", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		order := &obsOrder{}

		leader := mocks.NewMockLeaderElector(ctrl)
		leader.EXPECT().
			IsLeader(gomock.Any()).
			DoAndReturn(func(context.Context) (bool, error) {
				order.add("leader")
				return true, nil
			})
		limiter := mocks.NewMockRateLimiter(ctrl)
		limiter.EXPECT().Wait(gomock.Any()).DoAndReturn(func(context.Context) error {
			order.add("ratelimit")
			return nil
		})
		var retryCtxDeadline, jobCtxDeadline bool

		job := jobdecorators.WrapJob(
			func(ctx context.Context) error {
				order.add("job")
				jobCtxDeadline = hasDeadline(ctx)
				return errBoom
			},
			jobdecorators.JobStackDeps{
				Leader:  leader,
				Limiter: limiter,
				Retrier: retrierRecording(ctrl, order, &retryCtxDeadline),
				Tracer:  orderTracer{order: order, markDeadline: true},
				Metrics: orderedMetrics(ctrl, order),
				Logger:  orderedLogger(ctrl, order),
				Name:    "job",
				Timeout: time.Minute,
			},
		)

		require.ErrorIs(t, job(context.Background()), errBoom)
		require.Equal(t, []string{
			"leader", "ratelimit", "retry", "timeout", "trace", "job", "metric", "log",
		}, collapseRuns(order.snapshot()))
		require.False(t, retryCtxDeadline, "the timeout is applied inside retry")
		require.True(t, jobCtxDeadline, "the job runs under the per-attempt timeout")
	})
}
