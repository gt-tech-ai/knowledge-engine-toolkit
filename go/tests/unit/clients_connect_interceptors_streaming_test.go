package unit_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/transport/connect/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
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

// TestRecoveryInterceptor_Streaming_ConvertsPanic tests that the recovery interceptor
// recovers a panic in a server-streaming handler and returns CodeInternal.
//
// Why this test is important:
//   - Previously, the recovery interceptor was unary-only, so a panic in a stream handler
//     (e.g. a streamed answer) unwound past the interceptor and crashed the serving
//     goroutine, taking down every connection on it. This pins that the streaming path is
//     now panic-safe, exactly like the unary path.
//
// What it tests:
//   - WrapStreamingHandler wrapping a panicking StreamingHandlerFunc returns a non-nil
//     error classified CodeInternal (the panic never escapes).
func TestRecoveryInterceptor_Streaming_ConvertsPanic(t *testing.T) {
	t.Parallel()
	interceptor := interceptors.RecoveryInterceptor(fixtures.NopLogger())
	conn := newStreamConn(t)

	next := func(context.Context, connect.StreamingHandlerConn) error {
		panic("stream handler boom")
	}
	err := interceptor.WrapStreamingHandler(next)(context.Background(), conn)

	require.Error(t, err, "a panic in the stream handler must be recovered into an error")
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
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

// TestRateLimitInterceptor_Streaming_ShedsAndAllows tests that the rate-limit interceptor
// sheds a server-streaming RPC when the limiter denies and admits it when allowed.
//
// Why this test is important:
//   - Previously, streamed RPCs bypassed rate limiting entirely, so a flood of streaming
//     opens could not be shed. This pins that a denied stream is rejected before the
//     handler runs, and an allowed stream reaches it.
//
// What it tests:
//   - Limiter denies → WrapStreamingHandler returns CodeResourceExhausted and the handler
//     never runs; limiter allows → the handler runs.
func TestRateLimitInterceptor_Streaming_ShedsAndAllows(t *testing.T) {
	t.Parallel()

	denied := interceptors.RateLimitInterceptor(fixtures.StubRateLimiter(false))
	err := denied.WrapStreamingHandler(
		func(context.Context, connect.StreamingHandlerConn) error {
			t.Error("handler must not run when the stream is rate-limited")
			return nil
		},
	)(
		context.Background(),
		newStreamConn(t),
	)
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))

	ran := false
	allowed := interceptors.RateLimitInterceptor(fixtures.StubRateLimiter(true))
	err = allowed.WrapStreamingHandler(
		func(context.Context, connect.StreamingHandlerConn) error {
			ran = true
			return nil
		},
	)(
		context.Background(),
		newStreamConn(t),
	)
	require.NoError(t, err)
	assert.True(t, ran, "an allowed stream must reach the handler")
}

// TestBulkheadInterceptor_Streaming_ShedsWhenFull tests that the bulkhead interceptor
// sheds a server-streaming RPC when the concurrency limit is full.
//
// Why this test is important:
//   - Previously, streamed RPCs took no bulkhead slot, so unbounded concurrent streams
//     could exhaust the backing resource pool. This pins that a stream occupies a slot
//     and is shed with ResourceExhausted when none is free. (A slot is held for the
//     stream's full lifetime — a documented tradeoff; size DefaultInternalMaxConcurrent
//     accordingly.)
//
// What it tests:
//   - With MaxConcurrent(1) held by an in-flight stream, a second stream is rejected with
//     CodeResourceExhausted and its handler never runs.
func TestBulkheadInterceptor_Streaming_ShedsWhenFull(t *testing.T) {
	t.Parallel()
	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
	require.NoError(t, err)
	ic := interceptors.BulkheadInterceptor(bh)

	holding := make(chan struct{})
	release := make(chan struct{})
	blocking := ic.WrapStreamingHandler(
		func(context.Context, connect.StreamingHandlerConn) error {
			holding <- struct{}{}
			<-release
			return nil
		},
	)
	go func() { _ = blocking(context.Background(), newStreamConn(t)) }()
	<-holding // the one slot is now occupied

	shed := ic.WrapStreamingHandler(
		func(context.Context, connect.StreamingHandlerConn) error {
			t.Error("handler must not run when the bulkhead is full")
			return nil
		},
	)
	err = shed(context.Background(), newStreamConn(t))
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))

	close(release)
}
