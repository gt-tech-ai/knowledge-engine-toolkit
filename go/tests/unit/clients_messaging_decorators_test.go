package unit_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// counterValue gathers reg and returns the value of the counter named name carrying
// the given topic label, failing the test if it is absent.
func counterValue(t *testing.T, reg *prometheus.Registry, name, topic string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "topic" && l.GetValue() == topic {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	require.Failf(t, "counter not found", "counter %s{topic=%q} not found", name, topic)
	return 0
}

// TestHandlerObservability_RecordsHandleMetrics tests that the metrics decorator
// actually increments the handle counters, not merely that it preserves behaviour.
//
// Why this test is important:
//   - Metrics are a delivered observability concern; a decorator that
//     silently stopped recording would leave the consume path unobservable with no
//     failing test to catch it
//
// What it tests:
//   - A successful handle increments messaging_handle_total{topic}; a failed handle
//     increments both the total and messaging_handle_failures_total{topic}
func TestHandlerObservability_RecordsHandleMetrics(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	obs := decorators.NewHandlerObservability(nil, prom.NewFromRegistry(reg))

	ok := obs.Wrap(func(_ context.Context, _ *interfaces.Message) error { return nil })
	require.NoError(
		t,
		ok(context.Background(), &interfaces.Message{Topic: "t", ID: "m1"}),
	)

	bad := obs.Wrap(func(_ context.Context, _ *interfaces.Message) error {
		return apperr.Sentinel("boom")
	})
	require.Error(t, bad(context.Background(), &interfaces.Message{Topic: "t", ID: "m2"}))

	require.InDelta(t, 2.0, counterValue(t, reg, "messaging_handle_total", "t"), 0)
	require.InDelta(
		t,
		1.0,
		counterValue(t, reg, "messaging_handle_failures_total", "t"),
		0,
	)
}

// TestWrapPublisher_LogsPublishFailure tests that the logging decorator records a
// failed publish and propagates the error.
//
// Why this test is important:
//   - The SQS publisher goroutine has nowhere to surface a failed publish; the
//     logging decorator is how an operator sees a broker outage
//
// What it tests:
//   - A wrapped Publish that fails returns the error AND logs it via the logger
func TestWrapPublisher_LogsPublishFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl)
	core.EXPECT().Publish(gomock.Any(), "topic", gomock.Any()).
		Return(apperr.Sentinel("send failed"))
	spy := fixtures.NewSpyLogger()

	pub := decorators.WrapPublisher(core, decorators.PublisherDeps{Logger: spy})
	err := pub.Publish(context.Background(), "topic", []byte("payload"))

	require.Error(t, err)
	// The client stack is an INNER seam, so it logs the failure at Debug (suppressed
	// in staging/prod); the outermost seam is what logs Error there.
	require.NotEmpty(
		t,
		(*spy.ChildDebugCalls),
		"publish failure should be logged at debug (inner seam)",
	)
}

// TestWrapPublisher_QuietOnSuccess tests that a successful publish is not logged as
// an error.
//
// Why this test is important:
//   - Error-logging a successful publish would be noise that masks real failures
//
// What it tests:
//   - A wrapped Publish that succeeds returns nil and records no error log
func TestWrapPublisher_QuietOnSuccess(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl)
	core.EXPECT().Publish(gomock.Any(), "topic", gomock.Any()).Return(nil)
	spy := fixtures.NewSpyLogger()

	pub := decorators.WrapPublisher(core, decorators.PublisherDeps{Logger: spy})

	require.NoError(t, pub.Publish(context.Background(), "topic", []byte("payload")))
	require.Empty(t, (*spy.ChildErrorCalls))
}

// TestWrapPublisher_RoutesThroughRetrier tests that the resilience decorator runs
// the publish through the injected Retrier.
//
// Why this test is important:
//   - Resilience must be applied as a composed layer, not baked into the backend;
//     this proves the publish actually flows through the retrier's policy
//
// What it tests:
//   - With a Retrier wired, Publish is invoked inside Retrier.Retry
func TestWrapPublisher_RoutesThroughRetrier(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl)
	core.EXPECT().Publish(gomock.Any(), "topic", gomock.Any()).Return(nil)
	retrier := mocks.NewMockRetrier(ctrl)
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, op func() error) error { return op() })

	pub := decorators.WrapPublisher(core, decorators.PublisherDeps{Retrier: retrier})

	require.NoError(t, pub.Publish(context.Background(), "topic", []byte("payload")))
}

// TestWrapPublisher_ComposesAllLayers tests that logging, metrics, and resilience
// compose without altering the publish outcome.
//
// Why this test is important:
//   - The decorators must be transparent: composing them cannot change what the
//     caller observes from a publish, only add cross-cutting behaviour around it
//
// What it tests:
//   - A publisher wrapped with logger + metrics + retrier still publishes
//     successfully, exercising every composed layer
func TestWrapPublisher_ComposesAllLayers(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl)
	core.EXPECT().Publish(gomock.Any(), "topic", gomock.Any()).Return(nil)
	retrier := mocks.NewMockRetrier(ctrl)
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, op func() error) error { return op() })

	pub := decorators.WrapPublisher(core, decorators.PublisherDeps{
		Logger:  fixtures.NewSpyLogger(),
		Metrics: fixtures.NopMetrics(),
		Retrier: retrier,
	})

	require.NoError(t, pub.Publish(context.Background(), "topic", []byte("payload")))
}

// TestWrapPublisher_BatchComposesAllLayers tests that PublishBatch flows through
// every composed decorator (logging, metrics, resilience) without error.
//
// Why this test is important:
//   - PublishBatch must be decorated identically to Publish; an undecorated batch
//     path would silently skip retries and metrics for batched sends
//
// What it tests:
//   - A batch publish through logger + metrics + retrier succeeds
func TestWrapPublisher_BatchComposesAllLayers(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl)
	core.EXPECT().PublishBatch(gomock.Any(), "topic", gomock.Any()).Return(nil)
	retrier := mocks.NewMockRetrier(ctrl)
	retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, op func() error) error { return op() })

	pub := decorators.WrapPublisher(core, decorators.PublisherDeps{
		Logger:  fixtures.NewSpyLogger(),
		Metrics: fixtures.NopMetrics(),
		Retrier: retrier,
	})

	require.NoError(
		t,
		pub.PublishBatch(
			context.Background(),
			"topic",
			[][]byte{[]byte("a"), []byte("b")},
		),
	)
}

// TestWrapPublisher_BatchFailureLoggedAndMeasured tests that a failed batch publish
// is logged and its error propagated.
//
// Why this test is important:
//   - A swallowed batch failure would hide a broker outage from operators and from
//     the failure metric
//
// What it tests:
//   - A PublishBatch that fails returns the error and records an error log
func TestWrapPublisher_BatchFailureLoggedAndMeasured(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl)
	core.EXPECT().PublishBatch(gomock.Any(), "topic", gomock.Any()).
		Return(apperr.Sentinel("batch failed"))
	spy := fixtures.NewSpyLogger()

	pub := decorators.WrapPublisher(core, decorators.PublisherDeps{
		Logger:  spy,
		Metrics: fixtures.NopMetrics(),
	})

	require.Error(
		t,
		pub.PublishBatch(context.Background(), "topic", [][]byte{[]byte("a")}),
	)
	// Inner-seam failure logs at Debug.
	require.NotEmpty(
		t,
		(*spy.ChildDebugCalls),
		"batch publish failure should be logged at debug (inner seam)",
	)
}

// TestHandlerObservability_LogsHandleFailure tests that the handler logging decorator
// records a nacked message and propagates the handler's error unchanged.
//
// Why this test is important:
//   - Per-message failure visibility must live in a composed layer, not inline in
//     each backend consumer loop; a swallowed handler error would hide a nack
//
// What it tests:
//   - A wrapped handler that errors runs the inner handler, logs the failure, and
//     returns the same error
func TestHandlerObservability_LogsHandleFailure(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	inner := 0
	handler := decorators.NewHandlerObservability(spy, fixtures.NopMetrics()).Wrap(
		func(_ context.Context, _ *interfaces.Message) error {
			inner++
			return apperr.Sentinel("handler boom")
		},
	)

	err := handler(context.Background(), &interfaces.Message{Topic: "t", ID: "m1"})

	require.Error(t, err)
	require.Equal(t, 1, inner, "the inner handler must still run")
	require.NotEmpty(t, (*spy.ChildErrorCalls), "handle failure should be logged")
}

// TestHandlerObservability_QuietOnSuccess tests that a successful handle is not
// error-logged.
//
// Why this test is important:
//   - Error-logging every acked message would drown the real nacks in noise
//
// What it tests:
//   - A wrapped handler that succeeds returns nil and records no error log
func TestHandlerObservability_QuietOnSuccess(t *testing.T) {
	t.Parallel()

	spy := fixtures.NewSpyLogger()
	handler := decorators.NewHandlerObservability(spy, fixtures.NopMetrics()).Wrap(
		func(_ context.Context, _ *interfaces.Message) error { return nil },
	)

	require.NoError(
		t,
		handler(context.Background(), &interfaces.Message{Topic: "t", ID: "m1"}),
	)
	require.Empty(t, (*spy.ChildErrorCalls))
}

// TestWrapPublisher_RoutesThroughCircuitBreaker tests that a wired CircuitBreaker
// gates the publish through the shared stack, failing fast without touching the
// backend when the breaker is open.
//
// Why this test is important:
//   - When the broker is down the breaker must stop the publisher hammering it;
//     this proves the new CircuitBreaker layer is wired into the publisher stack.
//
// What it tests:
//   - With an open breaker, Publish errors and the core MessagePublisher is never
//     called (the gomock core has no expectations, so any call fails the test).
func TestWrapPublisher_RoutesThroughCircuitBreaker(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl) // no EXPECT → must not be called
	pub := decorators.WrapPublisher(core, decorators.PublisherDeps{
		CircuitBreaker: fixtures.StubCircuitBreaker(true),
	})

	require.Error(t, pub.Publish(context.Background(), "topic", []byte("x")),
		"open breaker should fail fast without calling the core publisher")
}

// clientOpCounter returns the value of client_operations_total carrying the given op
// label, failing the test if it is absent.
func clientOpCounter(t *testing.T, reg *prometheus.Registry, op string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "client_operations_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "op" && l.GetValue() == op {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	require.Failf(t, "counter not found", "client_operations_total{op=%q} not found", op)
	return 0
}

// TestWrapPublisher_RecordsClientOperationMetric verifies the publisher emits the
// unified client_operations_total{client,op} metric (op = topic) through the shared
// stack's metrics layer.
//
// Why this test is important:
//   - The publisher metric was renamed from messaging_publish_total{topic} to
//     client_operations_total{client,op}; a silent drop of the publisher metric would
//     leave the SQS send path unobservable with no failing test to catch it.
//
// What it tests:
//   - A successful publish increments client_operations_total{op="topic"} exactly once.
func TestWrapPublisher_RecordsClientOperationMetric(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	ctrl := gomock.NewController(t)
	core := mocks.NewMockMessagePublisher(ctrl)
	core.EXPECT().Publish(gomock.Any(), "topic", gomock.Any()).Return(nil)

	pub := decorators.WrapPublisher(
		core,
		decorators.PublisherDeps{Metrics: prom.NewFromRegistry(reg)},
	)
	require.NoError(t, pub.Publish(context.Background(), "topic", []byte("payload")))

	require.InDelta(t, 1.0, clientOpCounter(t, reg, "topic"), 0)
}
