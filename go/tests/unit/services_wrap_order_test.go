package unit_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestServiceDecorator_WrapOrder_TracingOutermost locks the observability
// order for the service builder: Tracing is the outermost of the Tracing → Metrics →
// Logging trio (Logging innermost).
//
// Why this test is important:
//   - Before the service builder had this inverted (Logging outermost), so a failure was
//     logged outside its own trace span. This test prevents a regression back to that
//     order — it would fail if Logging (or Metrics) were reordered outside Tracing.
//
// What it tests:
//   - On a Get, the tracing span starts before the logging entry (Tracing outside
//     Logging) and metrics are recorded (the trio is active).
func TestServiceDecorator_WrapOrder_TracingOutermost(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	// order records the sequence in which the observability trio touches the request:
	// "trace" when the tracer starts a span, "metric" when a metric is recorded, "log"
	// when the logger writes — so the test asserts the composed wrap order (Tracing
	// outermost, Logging innermost) purely from the recorded call sequence. All appends
	// run synchronously within the single svc.Get below, so no lock is needed.
	var order []string

	// Logger: every write (Debug on entry, Error on failure) records a "log" tag,
	// mirroring the trio's logging leg.
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order = append(order, "log") }).AnyTimes()
	logger.EXPECT().Error(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { order = append(order, "log") }).AnyTimes()

	// Metrics: the counter increment and histogram observation each record a "metric"
	// tag, mirroring the trio's metrics leg. Counter is requested twice at build time
	// (operations + errors), Histogram once; all resolve to these recording mocks.
	counter := mocks.NewMockCounter(ctrl)
	counter.EXPECT().Inc(gomock.Any(), gomock.Any()).
		Do(func(...string) { order = append(order, "metric") }).AnyTimes()
	hist := mocks.NewMockHistogram(ctrl)
	hist.EXPECT().Observe(gomock.Any(), gomock.Any(), gomock.Any()).
		Do(func(float64, ...string) { order = append(order, "metric") }).AnyTimes()
	metrics := mocks.NewMockMetrics(ctrl)
	metrics.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).AnyTimes()
	metrics.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).AnyTimes()

	// Tracer: starting a span records a "trace" tag and echoes the context; the span
	// itself is a no-op recorder. This is the trio's tracing leg.
	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().End().AnyTimes()
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	tracer := mocks.NewMockTracer(ctrl)
	tracer.EXPECT().Start(gomock.Any(), gomock.Any()).
		DoAndReturn(
			func(
				ctx context.Context,
				_ string,
				_ ...interfaces.SpanOption,
			) (context.Context, interfaces.Span) {
				order = append(order, "trace")
				return ctx, span
			},
		).AnyTimes()

	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithLogging(logger).WithMetrics(metrics).WithTracing(tracer).Build()

	_, err := svc.Get(context.Background(), "1")
	require.NoError(t, err)

	tags := order
	require.NotEmpty(t, tags, "the trio must record trace/metric/log tags")
	require.Equal(
		t,
		"trace",
		tags[0],
		"tracing span must start first (Tracing outermost of the trio)",
	)

	traceIdx := slices.Index(tags, "trace")
	logIdx := slices.Index(tags, "log")
	require.NotEqual(t, -1, logIdx, "the logging decorator must record")
	require.Less(
		t,
		traceIdx,
		logIdx,
		"tracing must start before the logging entry (Tracing outside Logging)",
	)
	require.Contains(t, tags, "metric", "the metrics decorator must record (trio active)")
}
