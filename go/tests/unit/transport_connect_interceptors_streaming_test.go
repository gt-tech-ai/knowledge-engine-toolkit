package unit_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/connect/interceptors"
)

// streamProcedure is the procedure the streaming-interceptor tests report from the
// mocked StreamingHandlerConn, so metrics/tracing/logging label it like a real RPC.
const streamProcedure = "/test.v1.StreamService/Stream"

// newStreamConn returns a mocked StreamingHandlerConn whose Spec() reports
// streamProcedure (the streaming interceptors read conn.Spec().Procedure), with the
// spec available any number of times so a test only asserts the behavior it cares about.
func newStreamConn(t *testing.T) *mocks.MockStreamingHandlerConn {
	t.Helper()
	conn := mocks.NewMockStreamingHandlerConn(gomock.NewController(t))
	conn.EXPECT().Spec().Return(connect.Spec{Procedure: streamProcedure}).AnyTimes()
	// Tracing reads the request headers (propagation + X-Request-ID) and writes the
	// response header (the traceresponse trace id); the others don't, so empty
	// headers available AnyTimes keep the helper reusable across all tests.
	conn.EXPECT().RequestHeader().Return(http.Header{}).AnyTimes()
	conn.EXPECT().ResponseHeader().Return(http.Header{}).AnyTimes()
	return conn
}

// TestMetricsInterceptor_Streaming_RecordsCounterAndDuration tests that the metrics
// interceptor records one request counter + one duration observation for a
// server-streaming RPC.
//
// Why this test is important:
//   - Previously, streaming RPCs emitted no metrics at all, so their request
//     rate, error rate, and latency were invisible on dashboards. This pins that a stream
//     is measured like a unary call: one counter increment (labeled with the terminal
//     code) and one duration observation, keyed on the procedure.
//
// What it tests:
//   - A successful streamed RPC increments the requests counter with (service, method,
//     "ok") and observes one duration for (service, method).
func TestMetricsInterceptor_Streaming_RecordsCounterAndDuration(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockMetrics(ctrl)
	counter := mocks.NewMockCounter(ctrl)
	hist := mocks.NewMockHistogram(ctrl)
	m.EXPECT().
		Counter("connect_rpc_requests_total", gomock.Any(), "service", "method", "code").
		Return(counter)
	m.EXPECT().
		Histogram(
			"connect_rpc_duration_seconds", gomock.Any(), gomock.Any(), "service", "method",
		).
		Return(hist)
	counter.EXPECT().Inc("test.v1.StreamService", "Stream", "ok")
	hist.EXPECT().Observe(gomock.Any(), "test.v1.StreamService", "Stream")

	interceptor := interceptors.MetricsInterceptor(m)
	conn := newStreamConn(t)
	next := func(context.Context, connect.StreamingHandlerConn) error { return nil }

	err := interceptor.WrapStreamingHandler(next)(context.Background(), conn)
	require.NoError(t, err)
}

// TestTracingInterceptor_Streaming_CreatesSpan tests that the tracing interceptor opens
// a server span for a server-streaming RPC.
//
// Why this test is important:
//   - Previously, a streamed RPC produced no span, so it was invisible in distributed
//     traces and couldn't be correlated with the calls it fanned out to. This pins that
//     the streaming path opens a span attributed to the RPC's service, like unary.
//
// What it tests:
//   - WrapStreamingHandler starts a span and sets the rpc.service attribute to the
//     stream's service (captured via the SpyTracer attribute hook).
func TestTracingInterceptor_Streaming_CreatesSpan(t *testing.T) {
	t.Parallel()
	var gotService string
	tracer := fixtures.SpyTracer(func(k string, v any) {
		if k == "rpc.service" {
			gotService, _ = v.(string)
		}
	})
	interceptor := interceptors.TracingInterceptor(tracer)
	conn := newStreamConn(t)
	next := func(context.Context, connect.StreamingHandlerConn) error { return nil }

	err := interceptor.WrapStreamingHandler(next)(context.Background(), conn)
	require.NoError(t, err)
	assert.Equal(t, "test.v1.StreamService", gotService,
		"the streaming path must open a span with the rpc.service attribute")
}

// TestLoggingInterceptor_Streaming_EmitsOneAccessLine tests that the logging interceptor
// emits exactly one access-log line for a successful server-streaming RPC.
//
// Why this test is important:
//   - Previously, streamed RPCs produced no access log, so a streamed request left no
//     audit trail. This pins one-line-per-stream (not zero, not one-per-message).
//
// What it tests:
//   - A successful streamed RPC produces exactly one Info access line and no error line.
func TestLoggingInterceptor_Streaming_EmitsOneAccessLine(t *testing.T) {
	t.Parallel()
	spy := fixtures.NewSpyLogger()
	interceptor := interceptors.NewLoggingInterceptor(spy)
	conn := newStreamConn(t)
	next := func(context.Context, connect.StreamingHandlerConn) error { return nil }

	err := interceptor.WrapStreamingHandler(next)(context.Background(), conn)
	require.NoError(t, err)
	// The interceptor logs via logger.WithContext(ctx) (for trace correlation), so the
	// access line lands on the child logger, exactly like the unary path.
	assert.Len(
		t,
		*spy.ChildInfoCalls,
		1,
		"a successful stream logs exactly one access line",
	)
	assert.Empty(t, *spy.ChildErrorCalls, "a successful stream logs no error line")
}
