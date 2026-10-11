package unit_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/goleak"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/grpc/interceptors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// signalMetrics builds a generated MockMetrics whose counter Inc pushes the last
// label value (the status code) onto recorded, letting a test deterministically wait
// for the async cancellation-watch goroutine (see metricsClientStream.watch) to run
// without polling.
func signalMetrics(ctrl *gomock.Controller, recorded chan string) *mocks.MockMetrics {
	m := mocks.NewMockMetrics(ctrl)
	counter := mocks.NewMockCounter(ctrl)
	hist := mocks.NewMockHistogram(ctrl)
	m.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).
		AnyTimes()
	m.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).AnyTimes()
	hist.EXPECT().Observe(gomock.Any(), gomock.Any()).AnyTimes()
	counter.EXPECT().Inc(gomock.Any()).Do(func(labelValues ...string) {
		code := ""
		if n := len(labelValues); n > 0 {
			code = labelValues[n-1]
		}
		recorded <- code
	}).AnyTimes()
	counter.EXPECT().Add(gomock.Any(), gomock.Any()).AnyTimes()
	return m
}

// signalSpan builds a generated MockSpan whose End closes ended (so a test can
// deterministically wait for the async cancellation-watch goroutine to run) and
// whose SetStatus records the last status code into the returned pointer — read
// race-free after <-ended via the channel-close happens-before, since finish sets
// the status before calling End.
func signalSpan(
	ctrl *gomock.Controller,
	ended chan struct{},
) (*mocks.MockSpan, *interfaces.SpanStatusCode) {
	sp := mocks.NewMockSpan(ctrl)
	st := new(interfaces.SpanStatusCode)
	sp.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	sp.EXPECT().RecordError(gomock.Any()).AnyTimes()
	sp.EXPECT().
		SetStatus(gomock.Any(), gomock.Any()).
		Do(func(code interfaces.SpanStatusCode, _ string) { *st = code }).AnyTimes()
	sp.EXPECT().End().Do(func() { close(ended) }).Times(1)
	return sp, st
}

// signalTracer builds a generated MockTracer that always hands out sp.
func signalTracer(ctrl *gomock.Controller, sp interfaces.Span) *mocks.MockTracer {
	tr := mocks.NewMockTracer(ctrl)
	tr.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			ctx context.Context,
			_ string,
			_ ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			return ctx, sp
		},
	).
		AnyTimes()
	return tr
}

// signalLogger builds a generated MockLogger whose Error pushes "error" onto logged,
// letting a test deterministically wait for the async cancellation-watch goroutine to
// run without polling. The success path logs via Info, which is intentionally a no-op
// here so a healthy stream records nothing on logged.
func signalLogger(ctrl *gomock.Controller, logged chan string) *mocks.MockLogger {
	lg := mocks.NewMockLogger(ctrl)
	lg.EXPECT().
		Error(gomock.Any(), gomock.Any()).
		Do(func(string, ...any) { logged <- "error" }).AnyTimes()
	lg.EXPECT().Info(gomock.Any(), gomock.Any()).AnyTimes()
	lg.EXPECT().Debug(gomock.Any(), gomock.Any()).AnyTimes()
	lg.EXPECT().Warn(gomock.Any(), gomock.Any()).AnyTimes()
	lg.EXPECT().With(gomock.Any()).Return(lg).AnyTimes()
	lg.EXPECT().WithContext(gomock.Any()).Return(lg).AnyTimes()
	return lg
}

// ---------------------------------------------------------------------------
// Recovery interceptor (server)
// ---------------------------------------------------------------------------

// TestGRPCRecoveryServerInterceptor_CatchesPanic tests that the gRPC server
// recovery interceptor converts handler panics into structured Internal errors.
//
// Why this test is important:
//   - Unrecovered panics crash the entire gRPC server, terminating all client connections
//   - The recovery interceptor is the last defense against handler bugs in production
//   - Structured error codes allow gRPC clients to distinguish server faults from client
//     errors
//
// What it tests:
//   - A panicking handler returns nil response and a codes.Internal gRPC error
func TestGRPCRecoveryServerInterceptor_CatchesPanic(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.RecoveryServerInterceptor(logger)

	resp, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			panic("test panic")
		},
	)
	assert.Nil(t, resp, "expected nil response after panic")
	require.Error(t, err, "expected error after panic")
	assert.Equal(t, codes.Internal, status.Code(err))
}

// TestGRPCRecoveryServerInterceptor_PassesThroughNormal tests that the
// recovery interceptor is transparent to successful requests.
//
// Why this test is important:
//   - The interceptor must add zero overhead or side effects to the happy path
//   - Response mutation by middleware would silently corrupt data for all gRPC handlers
//
// What it tests:
//   - A non-panicking handler's response passes through unchanged with no error
func TestGRPCRecoveryServerInterceptor_PassesThroughNormal(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.RecoveryServerInterceptor(logger)

	expected := "result"
	resp, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			return expected, nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestGRPCRecoveryServerInterceptor_PassesThroughError tests that the
// recovery interceptor preserves original handler error codes without interference.
//
// Why this test is important:
//   - gRPC status codes carry semantic meaning that clients use for retry and fallback
//     decisions
//   - Re-wrapping errors as Internal would mask the true cause and break client logic
//
// What it tests:
//   - A handler returning codes.NotFound propagates as NotFound (not swallowed or
//     re-wrapped)
func TestGRPCRecoveryServerInterceptor_PassesThroughError(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.RecoveryServerInterceptor(logger)

	_, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			return nil, status.Errorf(codes.NotFound, "not found")
		},
	)
	require.Error(t, err, "expected error to pass through")
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// ---------------------------------------------------------------------------
// Rate limit interceptor (server)
// ---------------------------------------------------------------------------

// TestGRPCRateLimitServerInterceptor_AllowsWithinLimit tests that requests
// within the rate limit pass through to the handler without interference.
//
// Why this test is important:
//   - A misconfigured rate limiter that rejects valid traffic causes false outages
//   - Confirms the interceptor delegates correctly to the rate limiter abstraction
//
// What it tests:
//   - A request allowed by the rate limiter returns the handler's response with no error
func TestGRPCRateLimitServerInterceptor_AllowsWithinLimit(t *testing.T) {
	t.Parallel()
	limiter := fixtures.StubRateLimiter(true)
	interceptor := grpcinterceptors.RateLimitServerInterceptor(limiter)

	expected := "result"
	resp, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			return expected, nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, expected, resp, "expected response to pass through")
}

// TestGRPCRateLimitServerInterceptor_RejectsWhenExceeded tests that requests
// exceeding the rate limit are rejected before reaching the handler.
//
// Why this test is important:
//   - Rate limiting protects downstream gRPC services from overload and resource
//     exhaustion
//   - The handler must not execute when the limit is exceeded to preserve server capacity
//   - Correct status codes allow gRPC clients to implement backoff strategies
//
// What it tests:
//   - A rate-limited request returns codes.ResourceExhausted without invoking the handler
func TestGRPCRateLimitServerInterceptor_RejectsWhenExceeded(t *testing.T) {
	t.Parallel()
	limiter := fixtures.StubRateLimiter(false)
	interceptor := grpcinterceptors.RateLimitServerInterceptor(limiter)

	_, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			require.FailNow(t, "handler should not be called when rate limited")
			return nil, status.Error(codes.Internal, "unreachable")
		},
	)
	require.Error(t, err, "expected error when rate limited")
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
}

// TestBulkheadServerInterceptor_ShedsWhenFull verifies the gRPC bulkhead
// interceptor rejects requests with ResourceExhausted once the concurrency limit
// is reached: with one request held in-flight against MaxConcurrent=1, a
// second is shed and its handler never runs.
func TestBulkheadServerInterceptor_ShedsWhenFull(t *testing.T) {
	t.Parallel()
	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
	require.NoError(t, err)
	ic := grpcinterceptors.BulkheadServerInterceptor(bh)
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

	holding := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = ic(
			context.Background(), nil, info,
			func(context.Context, any) (any, error) {
				holding <- struct{}{} // signal the one slot is held
				<-release
				return "ok", nil
			},
		)
	}()
	<-holding // the one slot is now occupied

	_, err = ic(
		context.Background(), nil, info,
		func(context.Context, any) (any, error) {
			t.Error("handler must not run when the bulkhead is full")
			return nil, status.Error(codes.Internal, "unreachable")
		},
	)
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))

	close(release) // let the in-flight request finish
}

// ---------------------------------------------------------------------------
// Logging interceptors (server + client)
// ---------------------------------------------------------------------------

// TestGRPCLoggingServerInterceptor_LogsSuccess tests that the server logging
// interceptor passes through successful responses without modification.
//
// Why this test is important:
//   - Logging must be a transparent decorator that never alters responses
//   - Response mutation by the logging layer would silently corrupt gRPC data
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestGRPCLoggingServerInterceptor_LogsSuccess(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.LoggingServerInterceptor(logger)

	resp, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, "ok", resp, "expected response to pass through")
}

// TestGRPCLoggingServerInterceptor_LogsError tests that the server logging
// interceptor propagates handler errors unchanged.
//
// Why this test is important:
//   - gRPC status codes carry semantic meaning used by clients for retry and fallback
//     decisions
//   - The logging layer must never re-wrap or swallow errors
//
// What it tests:
//   - A handler returning codes.Internal propagates as an error through the logging
//     interceptor
func TestGRPCLoggingServerInterceptor_LogsError(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.LoggingServerInterceptor(logger)

	_, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			return nil, status.Errorf(codes.Internal, "boom")
		},
	)
	require.Error(t, err, "expected error to pass through")
}

// TestGRPCLoggingClientInterceptor_LogsSuccess tests that the client logging
// interceptor passes through successful calls without modification.
//
// Why this test is important:
//   - Client-side logging must be transparent and never interfere with outgoing calls
//   - A broken logging interceptor could silently drop or alter inter-service
//     communication
//
// What it tests:
//   - A successful invoker call completes with no error
func TestGRPCLoggingClientInterceptor_LogsSuccess(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.LoggingClientInterceptor(logger)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return nil
		},
	)
	require.NoError(t, err, "unexpected error")
}

// TestGRPCLoggingClientInterceptor_LogsError tests that the client logging
// interceptor propagates invoker errors unchanged.
//
// Why this test is important:
//   - Error codes must pass through unmodified so callers can react appropriately
//   - Client-side logging must not re-wrap or swallow errors from downstream services
//
// What it tests:
//   - An invoker returning codes.Internal propagates as an error through the logging
//     interceptor
func TestGRPCLoggingClientInterceptor_LogsError(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.LoggingClientInterceptor(logger)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return status.Errorf(codes.Internal, "boom")
		},
	)
	require.Error(t, err, "expected error to pass through")
}

// ---------------------------------------------------------------------------
// Metrics interceptors (server + client)
// ---------------------------------------------------------------------------

// TestGRPCMetricsServerInterceptor_RecordsMetrics tests that the server
// metrics interceptor passes through successful responses without modification.
//
// Why this test is important:
//   - Metrics collection must be transparent and never alter the response
//   - A broken metrics interceptor could silently corrupt responses for all gRPC handlers
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestGRPCMetricsServerInterceptor_RecordsMetrics(t *testing.T) {
	t.Parallel()
	metrics := fixtures.NopMetrics()
	interceptor := grpcinterceptors.MetricsServerInterceptor(metrics)

	resp, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, "ok", resp, "expected response to pass through")
}

// TestGRPCMetricsClientInterceptor_RecordsMetrics tests that the client
// metrics interceptor passes through successful calls without modification.
//
// Why this test is important:
//   - Client-side metrics collection must be transparent to outgoing calls
//   - A broken metrics interceptor could silently interfere with inter-service
//     communication
//
// What it tests:
//   - A successful invoker call completes with no error
func TestGRPCMetricsClientInterceptor_RecordsMetrics(t *testing.T) {
	t.Parallel()
	metrics := fixtures.NopMetrics()
	interceptor := grpcinterceptors.MetricsClientInterceptor(metrics)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return nil
		},
	)
	require.NoError(t, err, "unexpected error")
}

// ---------------------------------------------------------------------------
// Tracing interceptors (server + client)
// ---------------------------------------------------------------------------

// TestGRPCTracingServerInterceptor_CreatesSpan tests that the server tracing
// interceptor passes through successful responses without modification.
//
// Why this test is important:
//   - Tracing must be a transparent decorator that never alters gRPC responses
//   - A broken tracing interceptor could silently corrupt data across all handlers
//
// What it tests:
//   - A successful handler response passes through unchanged with no error
func TestGRPCTracingServerInterceptor_CreatesSpan(t *testing.T) {
	t.Parallel()
	tracer := fixtures.NopTracer()
	interceptor := grpcinterceptors.TracingServerInterceptor(tracer)

	resp, err := interceptor(
		context.Background(),
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(_ context.Context, _ any) (any, error) {
			return "ok", nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, "ok", resp, "expected response to pass through")
}

// TestGRPCTracingClientInterceptor_CreatesSpan tests that the client tracing
// interceptor passes through successful outgoing calls without modification.
//
// Why this test is important:
//   - Client-side tracing must be transparent to outgoing calls
//   - Trace span creation must not interfere with inter-service communication
//
// What it tests:
//   - A successful invoker call completes with no error
func TestGRPCTracingClientInterceptor_CreatesSpan(t *testing.T) {
	t.Parallel()
	tracer := fixtures.NopTracer()
	interceptor := grpcinterceptors.TracingClientInterceptor(tracer)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return nil
		},
	)
	require.NoError(t, err, "unexpected error")
}

// sampledSpanContext returns a valid, sampled W3C span context to seed a ctx with,
// so the propagator has an active trace to inject.
func sampledSpanContext() oteltrace.SpanContext {
	return oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: oteltrace.TraceID{
			0x01,
			0x02,
			0x03,
			0x04,
			0x05,
			0x06,
			0x07,
			0x08,
			0x09,
			0x0a,
			0x0b,
			0x0c,
			0x0d,
			0x0e,
			0x0f,
			0x10,
		},
		SpanID:     oteltrace.SpanID{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18},
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
}

// TestGRPCTracingClientInterceptor_InjectsTraceparent tests that the unary client
// tracing interceptor propagates the active trace over the wire as gRPC metadata.
//
// Why this test is important:
//   - Without on-the-wire injection a client span is created but never propagated,
//     so the callee starts a new, disconnected trace and a single request fragments
//     into one trace per service. The regression this guards (a callee running on
//     its own trace, not its caller's) is exactly what breaks cross-service
//     correlation.
//
// What it tests:
//   - After the interceptor runs, the invoker's context carries a W3C `traceparent`
//     metadata entry that names the active trace id.
func TestGRPCTracingClientInterceptor_InjectsTraceparent(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	sc := sampledSpanContext()
	ctx := oteltrace.ContextWithSpanContext(context.Background(), sc)
	interceptor := grpcinterceptors.TracingClientInterceptor(fixtures.NopTracer())

	var got metadata.MD
	err := interceptor(
		ctx,
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			ic context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			got, _ = metadata.FromOutgoingContext(ic)
			return nil
		},
	)
	require.NoError(t, err)
	tp := got.Get("traceparent")
	require.NotEmpty(t, tp, "traceparent must be injected into outgoing gRPC metadata")
	assert.Contains(
		t,
		tp[0],
		sc.TraceID().String(),
		"traceparent carries the active trace id",
	)
}

// TestGRPCTracingStreamClientInterceptor_InjectsTraceparent tests that the STREAM
// client tracing interceptor propagates the active trace over the wire — the path
// that matters for server-streaming RPCs.
//
// Why this test is important:
//   - A server-streaming RPC has only the stream interceptor on its path; if
//     injection were added to the unary interceptor alone (as it first was), a
//     streaming callee would still start a disconnected trace.
//
// What it tests:
//   - After the stream interceptor runs, the streamer's context carries a W3C
//     `traceparent` naming the active trace id.
func TestGRPCTracingStreamClientInterceptor_InjectsTraceparent(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	sc := sampledSpanContext()
	ctx := oteltrace.ContextWithSpanContext(context.Background(), sc)
	interceptor := grpcinterceptors.TracingStreamClientInterceptor(fixtures.NopTracer())

	var got metadata.MD
	_, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Stream",
		func(
			sc context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			got, _ = metadata.FromOutgoingContext(sc)
			return nil, status.Error(codes.Unavailable, "stop before span bookkeeping")
		},
	)
	require.Error(t, err) // the streamer we injected returns an error by design
	tp := got.Get("traceparent")
	require.NotEmpty(t, tp, "traceparent must be injected into outgoing gRPC metadata")
	assert.Contains(
		t,
		tp[0],
		sc.TraceID().String(),
		"traceparent carries the active trace id",
	)
}

// ---------------------------------------------------------------------------
// Timeout interceptor (client)
// ---------------------------------------------------------------------------

// TestGRPCTimeoutClientInterceptor_CompletesBeforeDeadline tests that the
// client timeout interceptor is transparent when the call completes in time.
//
// Why this test is important:
//   - The timeout interceptor must not interfere with normal-speed calls
//   - Premature cancellation would cause spurious failures under healthy conditions
//
// What it tests:
//   - A fast invoker call completes with no error
func TestGRPCTimeoutClientInterceptor_CompletesBeforeDeadline(t *testing.T) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutClientInterceptor(time.Second)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return nil
		},
	)
	require.NoError(t, err, "unexpected error")
}

// TestGRPCTimeoutClientInterceptor_ExceedsDeadline tests that the client
// timeout interceptor cancels slow calls and returns a deadline-exceeded error.
//
// Why this test is important:
//   - Unbounded client latency ties up goroutines and can cascade into caller timeouts
//   - The correct status code enables callers to distinguish timeouts from other failures
//
// What it tests:
//   - An invoker that blocks past the deadline returns codes.DeadlineExceeded
func TestGRPCTimeoutClientInterceptor_ExceedsDeadline(t *testing.T) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutClientInterceptor(time.Millisecond)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			ctx context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)
	require.Error(t, err, "expected error on timeout")
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

// TestGRPCTimeoutClientInterceptor_CancelledContextReturnsCanceled tests that a
// call that ends because its parent context was cancelled reports codes.Canceled
// and is NOT relabeled as a "request timed out" deadline.
//
// Why this test is important:
//   - Conflating a caller-side cancellation (or a fast-fail on an already-done
//     context) with a real timeout misreports the failure as a full <timeout>
//     wait, corrupting latency diagnosis — an 18ms cancel logged as "timed out
//     after 5s" sends an operator chasing a phantom slow dependency.
//   - Callers switch on the status code, so a cancellation must surface as
//     Canceled, not DeadlineExceeded.
//
// What it tests:
//   - A pre-cancelled parent context yields codes.Canceled and a message that
//     does not claim a timeout
func TestGRPCTimeoutClientInterceptor_CancelledContextReturnsCanceled(t *testing.T) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutClientInterceptor(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller/upstream goes away before the call resolves

	err := interceptor(
		ctx,
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			ctx context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return ctx.Err()
		},
	)
	require.Error(t, err, "expected error on cancelled context")
	assert.Equal(
		t,
		codes.Canceled,
		status.Code(err),
		"a cancellation must not be reported as a timeout",
	)
	assert.NotContains(
		t,
		status.Convert(err).Message(),
		"timed out",
		"must not mislabel a cancellation as a timeout",
	)
}

// TestGRPCTimeoutClientInterceptor_PassesThroughNonContextError tests that an
// ordinary downstream error (with deadline budget to spare) is returned
// unchanged rather than relabeled as a timeout.
//
// Why this test is important:
//   - The timeout interceptor must be transparent to real application errors;
//     relabeling an Internal fault as a timeout hides the true failure and
//     misleads callers that switch on the status code.
//
// What it tests:
//   - A fast invoker returning codes.Internal is propagated unchanged
func TestGRPCTimeoutClientInterceptor_PassesThroughNonContextError(t *testing.T) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutClientInterceptor(time.Minute)

	sentinel := status.Error(codes.Internal, "boom")
	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return sentinel
		},
	)
	require.Error(t, err)
	assert.Equal(
		t,
		codes.Internal,
		status.Code(err),
		"a non-context error must pass through unchanged",
	)
}

// ---------------------------------------------------------------------------
// Circuit breaker interceptor (client)
// ---------------------------------------------------------------------------

// TestGRPCCircuitBreakerClientInterceptor_PassesWhenClosed tests that the
// client circuit breaker allows calls through when the circuit is closed.
//
// Why this test is important:
//   - The circuit breaker must be transparent during healthy operation
//   - Incorrectly blocking calls when the circuit is closed would cause false outages
//
// What it tests:
//   - A call with a closed circuit completes with no error
func TestGRPCCircuitBreakerClientInterceptor_PassesWhenClosed(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(false)
	interceptor := grpcinterceptors.CircuitBreakerClientInterceptor(cb)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return nil
		},
	)
	require.NoError(t, err, "unexpected error")
}

// TestGRPCCircuitBreakerClientInterceptor_RejectsWhenOpen tests that the
// client circuit breaker fast-fails calls when the circuit is open.
//
// Why this test is important:
//   - Open-circuit fast-fail prevents cascading failures to a degraded dependency
//   - The invoker must not execute to avoid wasting resources on a known-bad path
//   - Unavailable status signals callers to use fallback or retry later
//
// What it tests:
//   - A call with an open circuit returns codes.Unavailable without invoking the
//     downstream service
func TestGRPCCircuitBreakerClientInterceptor_RejectsWhenOpen(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(true)
	interceptor := grpcinterceptors.CircuitBreakerClientInterceptor(cb)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			require.FailNow(t, "invoker should not be called when circuit is open")
			return nil
		},
	)
	require.Error(t, err, "expected error when circuit is open")
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// TestGRPCCircuitBreakerClientInterceptor_PropagatesInnerError tests that
// invoker errors propagate unchanged when the circuit is closed.
//
// Why this test is important:
//   - Masking invoker errors as circuit-breaker errors would hide the true failure cause
//   - Callers need accurate status codes for correct retry and fallback decisions
//
// What it tests:
//   - An invoker returning codes.NotFound propagates as NotFound through the closed
//     circuit
func TestGRPCCircuitBreakerClientInterceptor_PropagatesInnerError(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(false)
	interceptor := grpcinterceptors.CircuitBreakerClientInterceptor(cb)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return status.Errorf(codes.NotFound, "not found")
		},
	)
	require.Error(t, err, "expected error to propagate")
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// ---------------------------------------------------------------------------
// Retry interceptor (client)
// ---------------------------------------------------------------------------

// TestGRPCRetryClientInterceptor_SucceedsFirstAttempt tests that the retry
// interceptor adds no overhead when the call succeeds on the first attempt.
//
// Why this test is important:
//   - The retry interceptor must be transparent to successful calls; wrapping
//     or delaying a first-attempt success would add latency to every request
//
// What it tests:
//   - A successful invoker call completes with no error
func TestGRPCRetryClientInterceptor_SucceedsFirstAttempt(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(0, false)
	interceptor := grpcinterceptors.RetryClientInterceptor(retrier)

	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			return nil
		},
	)
	require.NoError(t, err, "unexpected error")
}

// TestGRPCRetryClientInterceptor_RetriesTransientError tests that the retry
// interceptor retries transient failures until the call succeeds.
//
// Why this test is important:
//   - Transient Unavailable errors from overloaded dependencies recover on
//     retry; without retries, every transient failure surfaces as a user error
//
// What it tests:
//   - An invoker failing twice with Unavailable then succeeding results in 3 total
//     attempts
//   - The final successful result returns no error
func TestGRPCRetryClientInterceptor_RetriesTransientError(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(2, false)
	interceptor := grpcinterceptors.RetryClientInterceptor(retrier)

	attempts := 0
	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			attempts++
			if attempts <= 2 {
				return status.Errorf(codes.Unavailable, "transient")
			}
			return nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, 3, attempts)
}

// TestGRPCRetryClientInterceptor_StopsOnPermanentError tests that the retry
// interceptor does not retry non-retryable errors.
//
// Why this test is important:
//   - Retrying permanent client errors (e.g. InvalidArgument) wastes resources
//     and adds latency for a result that will never change
//
// What it tests:
//   - An invoker returning InvalidArgument triggers exactly 1 attempt (no retries)
//   - The original error code is preserved
func TestGRPCRetryClientInterceptor_StopsOnPermanentError(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(5, false)
	interceptor := grpcinterceptors.RetryClientInterceptor(retrier)

	attempts := 0
	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			attempts++
			return status.Errorf(codes.InvalidArgument, "bad request")
		},
	)
	require.Error(t, err, "expected error for permanent failure")
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, 1, attempts)
}

// TestGRPCRetryClientInterceptor_StopsOnResourceExhausted verifies that a
// rate-limited (ResourceExhausted) status is not retried.
//
// Why this test is important:
//   - Retrying a rate-limited dependency immediately only deepens the backpressure
//     and can escalate a brief throttle into a self-inflicted outage.
//
// What it tests:
//   - An invoker returning ResourceExhausted triggers exactly 1 attempt; the code
//     is preserved.
func TestGRPCRetryClientInterceptor_StopsOnResourceExhausted(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(5, false)
	interceptor := grpcinterceptors.RetryClientInterceptor(retrier)

	attempts := 0
	err := interceptor(
		context.Background(),
		"/test.Service/Method",
		nil,
		nil,
		nil,
		func(
			_ context.Context,
			_ string,
			_,
			_ any,
			_ *grpc.ClientConn,
			_ ...grpc.CallOption,
		) error {
			attempts++
			return status.Errorf(codes.ResourceExhausted, "rate limited")
		},
	)
	require.Error(t, err, "expected error for rate-limited failure")
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Equal(t, 1, attempts, "ResourceExhausted must not be retried")
}

// TestGRPCClientBuilder_BreakerWrapsRetry_OpenFailsFast verifies the gRPC client
// the builder ACTUALLY produces places the circuit breaker OUTSIDE retry.
//
// Why this test is important:
//   - With retry outside the breaker, an open breaker's Unavailable is retried
//     with backoff, hammering the dependency the breaker tripped to protect.
//     Driving this through a real (lazy) client conn built from Build()'s dial
//     options catches a chain() reorder that would reintroduce the bug.
//
// What it tests:
//   - A conn built with WithCircuitBreaker(open)+WithRetry returns Unavailable on
//     Invoke and never reaches the retrier (which never dials).
func TestGRPCClientBuilder_BreakerWrapsRetry_OpenFailsFast(t *testing.T) {
	t.Parallel()
	retrier, attempts := fixtures.StubRetrier(5, false)
	openCB := fixtures.StubCircuitBreaker(true)

	opts := grpcinterceptors.NewClientBuilder().
		WithCircuitBreaker(openCB).
		WithRetry(retrier).
		Build()

	conn, err := grpc.NewClient(
		"passthrough:///breaker-test",
		append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))...,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// The conn is lazy; Invoke runs the interceptor chain, where the open breaker
	// (outermost) short-circuits before any dial or marshal.
	err = conn.Invoke(context.Background(), "/test.Service/Method", nil, nil)
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(
		t,
		0,
		attempts(),
		"the built conn must place the breaker outside retry: an open breaker fails fast "+
			"without reaching the retrier",
	)
}

// ---------------------------------------------------------------------------
// ServerBuilder
// ---------------------------------------------------------------------------

// TestGRPCServerBuilder_EmptyBuildReturnsNil tests that building a gRPC
// ServerBuilder with no interceptors configured returns nil.
//
// Why this test is important:
//   - Services that add no interceptors must not get a non-nil but empty chain;
//     passing an empty ChainUnaryInterceptor causes hard-to-debug registration errors
//
// What it tests:
//   - An empty ServerBuilder produces nil server options
func TestGRPCServerBuilder_EmptyBuildReturnsNil(t *testing.T) {
	t.Parallel()
	opts := grpcinterceptors.NewServerBuilder().Build()
	assert.Nil(t, opts, "expected nil options from empty builder")
}

// TestGRPCServerBuilder_FullComposition tests that the gRPC ServerBuilder
// composes all available server interceptors into a single server option.
//
// Why this test is important:
//   - The builder must produce exactly one chained option regardless of how many
//     interceptors are added; multiple separate options would apply in the wrong order
//
// What it tests:
//   - A fully-configured ServerBuilder produces exactly 1 non-nil ChainUnaryInterceptor
//     option
func TestGRPCServerBuilder_FullComposition(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	metrics := fixtures.NopMetrics()
	tracer := fixtures.NopTracer()
	limiter := fixtures.StubRateLimiter(true)
	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
	require.NoError(t, err)

	opts := grpcinterceptors.NewServerBuilder().
		WithRecovery().
		WithRateLimit(limiter).
		WithBulkhead(bh).
		WithMetrics(metrics).
		WithTracing(tracer).
		WithLogging(logger).
		Build()

	require.NotNil(t, opts, "expected non-nil options from full builder")
	assert.Len(t, opts, 1)
}

// TestGRPCServerBuilder_RecoveryRequiresLogger tests that the gRPC recovery
// interceptor is silently omitted when no logger is provided.
//
// Why this test is important:
//   - Adding recovery without a logger would cause a nil-pointer panic when the
//     recovery handler tries to log the panic; silent omission is the safe default
//
// What it tests:
//   - WithRecovery without WithLogging produces nil options (recovery not added)
func TestGRPCServerBuilder_RecoveryRequiresLogger(t *testing.T) {
	t.Parallel()
	opts := grpcinterceptors.NewServerBuilder().
		WithRecovery().
		Build()

	assert.Nil(t, opts, "expected nil options when recovery is set without logger")
}

// ---------------------------------------------------------------------------
// ClientBuilder
// ---------------------------------------------------------------------------

// TestGRPCClientBuilder_EmptyBuildReturnsNil tests that building a gRPC
// ClientBuilder with no interceptors configured returns nil.
//
// Why this test is important:
//   - Clients that add no interceptors must not get a non-nil but empty chain;
//     passing an empty WithChainUnaryInterceptor causes confusing dial-time errors
//
// What it tests:
//   - An empty ClientBuilder produces nil dial options
func TestGRPCClientBuilder_EmptyBuildReturnsNil(t *testing.T) {
	t.Parallel()
	opts := grpcinterceptors.NewClientBuilder().Build()
	assert.Nil(t, opts, "expected nil options from empty builder")
}

// TestGRPCClientBuilder_FullComposition tests that the gRPC ClientBuilder
// composes all available client interceptors into a single dial option.
//
// Why this test is important:
//   - The full middleware stack must be assembleable without errors; a missing
//     interceptor or wrong composition order would silently bypass observability
//
// What it tests:
//   - A fully-configured ClientBuilder produces exactly 1 non-nil
//     WithChainUnaryInterceptor option
func TestGRPCClientBuilder_FullComposition(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	metrics := fixtures.NopMetrics()
	tracer := fixtures.NopTracer()
	retrier, _ := fixtures.StubRetrier(0, false)
	cb := fixtures.StubCircuitBreaker(false)

	opts := grpcinterceptors.NewClientBuilder().
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
// Timeout interceptor (client stream)
// ---------------------------------------------------------------------------

// TestGRPCTimeoutStreamClientInterceptor_CompletesBeforeDeadline tests that
// the client stream timeout interceptor is transparent once a stream opens
// within the deadline — it does not wrap or otherwise interfere with the
// returned stream's later Recv behavior.
//
// Why this test is important:
//   - The timeout interceptor must not interfere with normal-speed streams
//   - Since the interceptor only bounds stream *creation* (never the stream's
//     data lifetime — a unary-style whole-call timeout would kill long-lived
//     streams), the returned stream must be handed back completely unwrapped
//
// What it tests:
//   - Once open succeeds, the stream's own clean end (io.EOF) is reported unchanged
func TestGRPCTimeoutStreamClientInterceptor_CompletesBeforeDeadline(t *testing.T) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutStreamClientInterceptor(time.Second)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")
	require.ErrorIs(t, stream.RecvMsg(nil), io.EOF, "expected a clean end of stream")
}

// TestGRPCTimeoutStreamClientInterceptor_OpenExceedsDeadline tests that the
// client stream timeout interceptor bounds only stream *creation*: a
// streamer call that never returns within the open-timeout yields
// codes.DeadlineExceeded, without waiting on the stream itself.
//
// Why this test is important:
//   - A hung stream-open (e.g. waiting on a dead connection) must not block
//     forever; the client needs a bounded signal to retry or trip a breaker
//   - This must be distinguishable from a data-lifetime timeout: the mock
//     streamer here would block indefinitely if not for the open-timeout,
//     modeling a hung open rather than a slow-but-healthy stream
//
// What it tests:
//   - A streamer that blocks past the open-timeout returns codes.DeadlineExceeded
//   - The abandoned streamer call observes the interceptor's cancellation
//     (rather than being leaked) via its own ctx.Done()
func TestGRPCTimeoutStreamClientInterceptor_OpenExceedsDeadline(t *testing.T) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutStreamClientInterceptor(10 * time.Millisecond)

	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			<-ctx.Done() // models a hung open, aborted by the interceptor
			return nil, ctx.Err()
		},
	)
	require.Error(t, err, "expected error on open timeout")
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

// TestGRPCTimeoutStreamClientInterceptor_CancelledParentReturnsCanceledAtOpen
// tests that a stream-open that fails because the caller's own context was
// already cancelled reports codes.Canceled and is NOT relabeled as a
// "request timed out" deadline.
//
// Why this test is important:
//   - Conflating a caller-side cancellation with a real timeout misreports the
//     failure as a full <timeout> wait, corrupting latency diagnosis
//   - Callers switch on the status code, so a cancellation must surface as
//     Canceled, not DeadlineExceeded
//
// What it tests:
//   - A pre-cancelled parent context yields codes.Canceled and a message that
//     does not claim a timeout, even though the configured open-timeout is long
func TestGRPCTimeoutStreamClientInterceptor_CancelledParentReturnsCanceledAtOpen(
	t *testing.T,
) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutStreamClientInterceptor(5 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller/upstream goes away before the open even starts

	_, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return nil, ctx.Err()
		},
	)
	require.Error(t, err, "expected error on cancelled context")
	assert.Equal(
		t,
		codes.Canceled,
		status.Code(err),
		"a cancellation must not be reported as a timeout",
	)
	assert.NotContains(
		t,
		status.Convert(err).Message(),
		"timed out",
		"must not mislabel a cancellation as a timeout",
	)
}

// TestGRPCTimeoutStreamClientInterceptor_PassesThroughNonContextError tests
// that an ordinary stream error (with deadline budget to spare) is returned
// unchanged rather than relabeled as a timeout.
//
// Why this test is important:
//   - The timeout interceptor must be transparent to real application errors;
//     relabeling an Internal fault as a timeout hides the true failure and
//     misleads callers that switch on the status code
//
// What it tests:
//   - A RecvMsg error unrelated to context expiry is propagated unchanged
func TestGRPCTimeoutStreamClientInterceptor_PassesThroughNonContextError(t *testing.T) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutStreamClientInterceptor(time.Minute)

	sentinel := status.Error(codes.Internal, "boom")
	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil, sentinel), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	err = stream.RecvMsg(nil)
	require.Error(t, err)
	assert.Equal(
		t,
		codes.Internal,
		status.Code(err),
		"a non-context error must pass through unchanged",
	)
}

// TestGRPCTimeoutStreamClientInterceptor_CreationFailsWithNonContextError
// tests that a streamer error at stream-creation time (with deadline budget
// to spare) is returned unchanged rather than relabeled as a timeout.
//
// Why this test is important:
//   - The timeout interceptor must be transparent to real creation-time
//     failures (e.g. a dial error), not just to errors surfaced later via RecvMsg
//
// What it tests:
//   - A streamer error unrelated to context expiry is propagated unchanged
func TestGRPCTimeoutStreamClientInterceptor_CreationFailsWithNonContextError(
	t *testing.T,
) {
	t.Parallel()
	interceptor := grpcinterceptors.TimeoutStreamClientInterceptor(time.Minute)

	sentinel := status.Error(codes.Internal, "boom")
	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			_ context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return nil, sentinel
		},
	)
	require.Error(t, err)
	assert.Equal(
		t,
		codes.Internal,
		status.Code(err),
		"a non-context creation error must pass through unchanged",
	)
}

// ---------------------------------------------------------------------------
// Circuit breaker interceptor (client stream)
// ---------------------------------------------------------------------------

// TestGRPCCircuitBreakerStreamClientInterceptor_PassesWhenClosed tests that
// the client stream circuit breaker allows stream creation through when the
// circuit is closed.
//
// Why this test is important:
//   - The circuit breaker must be transparent during healthy operation
//   - Incorrectly blocking streams when the circuit is closed would cause false outages
//
// What it tests:
//   - Stream creation with a closed circuit completes with no error
func TestGRPCCircuitBreakerStreamClientInterceptor_PassesWhenClosed(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(false)
	interceptor := grpcinterceptors.CircuitBreakerStreamClientInterceptor(cb)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.NotNil(t, stream, "expected stream to pass through")
}

// TestGRPCCircuitBreakerStreamClientInterceptor_RejectsWhenOpen tests that
// the client stream circuit breaker fast-fails stream creation when the
// circuit is open.
//
// Why this test is important:
//   - Open-circuit fast-fail prevents cascading failures to a degraded dependency
//   - The streamer must not execute to avoid wasting resources on a known-bad path
//   - Unavailable status signals callers to use fallback or retry later
//
// What it tests:
//   - Stream creation with an open circuit returns codes.Unavailable without invoking
//     streamer
func TestGRPCCircuitBreakerStreamClientInterceptor_RejectsWhenOpen(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(true)
	interceptor := grpcinterceptors.CircuitBreakerStreamClientInterceptor(cb)

	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			_ context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			require.FailNow(t, "streamer should not be called when circuit is open")
			return nil, status.Error(codes.Internal, "unreachable")
		},
	)
	require.Error(t, err, "expected error when circuit is open")
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// TestGRPCCircuitBreakerStreamClientInterceptor_PropagatesInnerError tests
// that streamer errors propagate unchanged when the circuit is closed.
//
// Why this test is important:
//   - Masking streamer errors as circuit-breaker errors would hide the true failure cause
//   - Callers need accurate status codes for correct retry and fallback decisions
//
// What it tests:
//   - A streamer returning codes.NotFound propagates as NotFound through the closed
//     circuit
func TestGRPCCircuitBreakerStreamClientInterceptor_PropagatesInnerError(t *testing.T) {
	t.Parallel()
	cb := fixtures.StubCircuitBreaker(false)
	interceptor := grpcinterceptors.CircuitBreakerStreamClientInterceptor(cb)

	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			_ context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return nil, status.Errorf(codes.NotFound, "not found")
		},
	)
	require.Error(t, err, "expected error to propagate")
	assert.Equal(t, codes.NotFound, status.Code(err))
}

// ---------------------------------------------------------------------------
// Retry interceptor (client stream)
// ---------------------------------------------------------------------------

// TestGRPCRetryStreamClientInterceptor_SucceedsFirstAttempt tests that the
// stream retry interceptor adds no overhead when stream creation succeeds on
// the first attempt.
//
// Why this test is important:
//   - The retry interceptor must be transparent to successful streams; wrapping
//     or delaying a first-attempt success would add latency to every request
//
// What it tests:
//   - A successful streamer call completes with no error
func TestGRPCRetryStreamClientInterceptor_SucceedsFirstAttempt(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(0, false)
	interceptor := grpcinterceptors.RetryStreamClientInterceptor(retrier)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.NotNil(t, stream)
}

// TestGRPCRetryStreamClientInterceptor_RetriesTransientError tests that the
// stream retry interceptor retries transient stream-creation failures until
// the stream is established.
//
// Why this test is important:
//   - Transient Unavailable errors from overloaded dependencies recover on
//     retry; without retries, every transient failure surfaces as a user error
//
// What it tests:
//   - A streamer failing twice with Unavailable then succeeding results in 3 total
//     attempts
//   - The final successful result returns no error
func TestGRPCRetryStreamClientInterceptor_RetriesTransientError(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(2, false)
	interceptor := grpcinterceptors.RetryStreamClientInterceptor(retrier)

	attempts := 0
	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			attempts++
			if attempts <= 2 {
				return nil, status.Errorf(codes.Unavailable, "transient")
			}
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, 3, attempts)
}

// TestGRPCRetryStreamClientInterceptor_StopsOnPermanentError tests that the
// stream retry interceptor does not retry non-retryable stream-creation
// errors.
//
// Why this test is important:
//   - Retrying permanent client errors (e.g. InvalidArgument) wastes resources
//     and adds latency for a result that will never change
//
// What it tests:
//   - A streamer returning InvalidArgument triggers exactly 1 attempt (no retries)
//   - The original error code is preserved
func TestGRPCRetryStreamClientInterceptor_StopsOnPermanentError(t *testing.T) {
	t.Parallel()
	retrier, _ := fixtures.StubRetrier(5, false)
	interceptor := grpcinterceptors.RetryStreamClientInterceptor(retrier)

	attempts := 0
	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			_ context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			attempts++
			return nil, status.Errorf(codes.InvalidArgument, "bad request")
		},
	)
	require.Error(t, err, "expected error for permanent failure")
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, 1, attempts)
}

// ---------------------------------------------------------------------------
// Metrics interceptor (client stream)
// ---------------------------------------------------------------------------

// TestGRPCMetricsStreamClientInterceptor_RecordsMetrics tests that the client
// stream metrics interceptor is transparent across the full stream lifecycle.
//
// Why this test is important:
//   - Metrics collection must be transparent and never alter stream data
//   - The interceptor must record on stream completion, not just creation,
//     since streamer returns before the stream actually finishes
//
// What it tests:
//   - A stream that completes cleanly reports io.EOF unchanged
func TestGRPCMetricsStreamClientInterceptor_RecordsMetrics(t *testing.T) {
	t.Parallel()
	metrics := fixtures.NopMetrics()
	interceptor := grpcinterceptors.MetricsStreamClientInterceptor(metrics)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error")
	require.ErrorIs(t, stream.RecvMsg(nil), io.EOF, "expected a clean end of stream")
}

// TestGRPCMetricsStreamClientInterceptor_RecordsCreationFailure tests that
// the client stream metrics interceptor records metrics and propagates the
// error unchanged when stream creation itself fails.
//
// Why this test is important:
//   - A stream that never establishes still needs to be counted; otherwise
//     failure-heavy dependencies would look artificially healthy in metrics
//
// What it tests:
//   - A streamer error at creation time is returned unchanged
func TestGRPCMetricsStreamClientInterceptor_RecordsCreationFailure(t *testing.T) {
	t.Parallel()
	metrics := fixtures.NopMetrics()
	interceptor := grpcinterceptors.MetricsStreamClientInterceptor(metrics)

	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			_ context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return nil, status.Errorf(codes.Unavailable, "unreachable")
		},
	)
	require.Error(t, err, "expected error to propagate")
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// TestGRPCMetricsStreamClientInterceptor_RecordsRecvMsgFailure tests that the
// client stream metrics interceptor records metrics and propagates the error
// unchanged when the established stream later fails.
//
// Why this test is important:
//   - Duration and error-code metrics must reflect the stream's actual
//     terminal outcome, not just whether it was created
//
// What it tests:
//   - A RecvMsg error on an established stream is returned unchanged
func TestGRPCMetricsStreamClientInterceptor_RecordsRecvMsgFailure(t *testing.T) {
	t.Parallel()
	metrics := fixtures.NopMetrics()
	interceptor := grpcinterceptors.MetricsStreamClientInterceptor(metrics)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(
				ctx,
				nil,
				status.Errorf(codes.Internal, "boom"),
			), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	err = stream.RecvMsg(nil)
	require.Error(t, err, "expected error to propagate")
	assert.Equal(t, codes.Internal, status.Code(err))
}

// ---------------------------------------------------------------------------
// Tracing interceptor (client stream)
// ---------------------------------------------------------------------------

// TestGRPCTracingStreamClientInterceptor_CreatesSpan tests that the client
// stream tracing interceptor is transparent across the full stream lifecycle.
//
// Why this test is important:
//   - Client-side tracing must be transparent to outgoing streams
//   - The span must be ended on stream completion, not just creation, since
//     streamer returns before the stream actually finishes
//
// What it tests:
//   - A stream that completes cleanly reports io.EOF unchanged
func TestGRPCTracingStreamClientInterceptor_CreatesSpan(t *testing.T) {
	t.Parallel()
	tracer := fixtures.NopTracer()
	interceptor := grpcinterceptors.TracingStreamClientInterceptor(tracer)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error")
	require.ErrorIs(t, stream.RecvMsg(nil), io.EOF, "expected a clean end of stream")
}

// TestGRPCTracingStreamClientInterceptor_EndsSpanOnCreationFailure tests that
// the client stream tracing interceptor ends the span and propagates the
// error unchanged when stream creation itself fails.
//
// Why this test is important:
//   - A span opened for a stream that never establishes must still be closed;
//     otherwise failed dial attempts would leak open spans
//
// What it tests:
//   - A streamer error at creation time is returned unchanged
func TestGRPCTracingStreamClientInterceptor_EndsSpanOnCreationFailure(t *testing.T) {
	t.Parallel()
	tracer := fixtures.NopTracer()
	interceptor := grpcinterceptors.TracingStreamClientInterceptor(tracer)

	_, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			_ context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return nil, status.Errorf(codes.Unavailable, "unreachable")
		},
	)
	require.Error(t, err, "expected error to propagate")
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// TestGRPCTracingStreamClientInterceptor_EndsSpanOnRecvMsgFailure tests that
// the client stream tracing interceptor records the error and ends the span
// when the established stream later fails.
//
// Why this test is important:
//   - The span's recorded error and status must reflect the stream's actual
//     terminal outcome, not just whether it was created
//
// What it tests:
//   - A RecvMsg error on an established stream is returned unchanged
func TestGRPCTracingStreamClientInterceptor_EndsSpanOnRecvMsgFailure(t *testing.T) {
	t.Parallel()
	tracer := fixtures.NopTracer()
	interceptor := grpcinterceptors.TracingStreamClientInterceptor(tracer)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(
				ctx,
				nil,
				status.Errorf(codes.Internal, "boom"),
			), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	err = stream.RecvMsg(nil)
	require.Error(t, err, "expected error to propagate")
	assert.Equal(t, codes.Internal, status.Code(err))
}

// ---------------------------------------------------------------------------
// Logging interceptor (client stream)
// ---------------------------------------------------------------------------

// TestGRPCLoggingStreamClientInterceptor_LogsSuccess tests that the client
// stream logging interceptor passes through a cleanly-completing stream
// without modification.
//
// Why this test is important:
//   - Client-side logging must be transparent and never interfere with outgoing streams
//   - A broken logging interceptor could silently drop or alter inter-service
//     communication
//
// What it tests:
//   - A stream that completes cleanly reports io.EOF unchanged
func TestGRPCLoggingStreamClientInterceptor_LogsSuccess(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.LoggingStreamClientInterceptor(logger)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error")
	require.ErrorIs(t, stream.RecvMsg(nil), io.EOF, "expected a clean end of stream")
}

// TestGRPCLoggingStreamClientInterceptor_LogsError tests that the client
// stream logging interceptor propagates a terminal RecvMsg error unchanged.
//
// Why this test is important:
//   - Error codes must pass through unmodified so callers can react appropriately
//   - Client-side logging must not re-wrap or swallow errors from downstream services
//
// What it tests:
//   - A stream whose RecvMsg returns codes.Internal propagates as an error through the
//     logging interceptor
func TestGRPCLoggingStreamClientInterceptor_LogsError(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	interceptor := grpcinterceptors.LoggingStreamClientInterceptor(logger)

	stream, err := interceptor(
		context.Background(),
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(
				ctx,
				nil,
				status.Errorf(codes.Internal, "boom"),
			), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	err = stream.RecvMsg(nil)
	require.Error(t, err, "expected error to propagate")
}

// ---------------------------------------------------------------------------
// StreamingClientBuilder
// ---------------------------------------------------------------------------

// TestGRPCStreamingClientBuilder_EmptyBuildReturnsNil tests that building a
// gRPC StreamingClientBuilder with no interceptors configured returns nil.
//
// Why this test is important:
//   - Clients that add no interceptors must not get a non-nil but empty chain;
//     passing an empty WithChainStreamInterceptor causes confusing dial-time errors
//
// What it tests:
//   - An empty StreamingClientBuilder produces nil dial options
func TestGRPCStreamingClientBuilder_EmptyBuildReturnsNil(t *testing.T) {
	t.Parallel()
	opts := grpcinterceptors.NewStreamingClientBuilder().Build()
	assert.Nil(t, opts, "expected nil options from empty builder")
}

// TestGRPCStreamingClientBuilder_FullComposition tests that the gRPC
// StreamingClientBuilder composes all available stream interceptors into a
// single dial option.
//
// Why this test is important:
//   - The full middleware stack must be assembleable without errors; a missing
//     interceptor or wrong composition order would silently bypass observability
//
// What it tests:
//   - A fully-configured StreamingClientBuilder produces exactly 1 non-nil
//     WithChainStreamInterceptor option
func TestGRPCStreamingClientBuilder_FullComposition(t *testing.T) {
	t.Parallel()
	logger := fixtures.NopLogger()
	metrics := fixtures.NopMetrics()
	tracer := fixtures.NopTracer()
	retrier, _ := fixtures.StubRetrier(0, false)
	cb := fixtures.StubCircuitBreaker(false)

	opts := grpcinterceptors.NewStreamingClientBuilder().
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

// TestGRPCStreamingClientBuilder_BreakerWrapsRetry_OpenFailsFast verifies the
// streaming builder ACTUALLY produces places the circuit breaker OUTSIDE
// retry, mirroring TestGRPCClientBuilder_BreakerWrapsRetry_OpenFailsFast
// for the unary builder.
//
// Why this test is important:
//   - With retry outside the breaker, an open breaker's Unavailable is retried
//     with backoff, hammering the dependency the breaker tripped to protect.
//     Driving this through a real (lazy) client conn built from Build()'s dial
//     options catches a chain() reorder that would reintroduce the bug.
//
// What it tests:
//   - A conn built with WithCircuitBreaker(open)+WithRetry returns Unavailable
//     on NewStream and never reaches the retrier (which never opens a stream).
func TestGRPCStreamingClientBuilder_BreakerWrapsRetry_OpenFailsFast(t *testing.T) {
	t.Parallel()
	retrier, attempts := fixtures.StubRetrier(5, false)
	openCB := fixtures.StubCircuitBreaker(true)

	opts := grpcinterceptors.NewStreamingClientBuilder().
		WithCircuitBreaker(openCB).
		WithRetry(retrier).
		Build()

	conn, err := grpc.NewClient(
		"passthrough:///streaming-breaker-test",
		append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))...,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// The conn is lazy; NewStream runs the interceptor chain, where the open
	// breaker (outermost of the two) short-circuits before the retrier ever
	// attempts to open the stream.
	_, err = conn.NewStream(
		context.Background(),
		&grpc.StreamDesc{},
		"/test.Service/Method",
	)
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(
		t,
		0,
		attempts(),
		"the built conn must place the breaker outside retry: an open breaker fails fast "+
			"without reaching the retrier",
	)
}

// TestGRPCStreamingClientBuilder_ComposesFullOrder verifies the streaming
// builder's ACTUAL runtime composition matches its documented order —
// timeout → metrics → circuit breaker → retry → tracing → logging — rather
// than just re-reading the Build() source, extending
// TestGRPCStreamingClientBuilder_BreakerWrapsRetry_OpenFailsFast (which only
// proves cb wraps retry) to all five interceptors with an observable hook.
//
// Why this test is important:
//   - A future edit could reorder two adjacent layers in Build() and still pass
//     every isolated-interceptor and cb-vs-retry test; only a test asserting the
//     complete sequence catches that class of regression
//   - Wrong order elsewhere (e.g. tracing outside retry) would span/log once
//     per logical operation instead of once per attempt, or vice versa,
//     silently corrupting observability without any functional test failing
//
// What it tests:
//   - Dialing a closed local listener makes stream creation fail immediately
//     and deterministically (no real server needed), and every configured
//     interceptor's dependency call fires in exactly the sequence implied by
//     the canonical order — cb, retry, tracing (on the way in) then logging,
//     metrics (on the way back out) — enforced by gomock.InOrder across the
//     five generated collaborator mocks.
func TestGRPCStreamingClientBuilder_ComposesFullOrder(t *testing.T) {
	t.Parallel()

	// A listener closed before any dial arrives makes NewStream fail with an
	// immediate, deterministic loopback connection error — every interceptor
	// still runs synchronously to completion, without needing a real server.
	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err, "failed to reserve a local port")
	addr := lis.Addr().String()
	require.NoError(t, lis.Close(), "failed to close listener before dial")

	ctrl := gomock.NewController(t)
	cb := mocks.NewMockCircuitBreaker(ctrl)
	retrier := mocks.NewMockRetrier(ctrl)
	tracer := mocks.NewMockTracer(ctrl)
	span := mocks.NewMockSpan(ctrl)
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	metrics := mocks.NewMockMetrics(ctrl)
	counter := mocks.NewMockCounter(ctrl)
	hist := mocks.NewMockHistogram(ctrl)

	// Non-order-critical wiring: the metrics interceptor builds its counter +
	// histogram at construction; the tracer hands out span; the span/histogram
	// no-ops absorb the calls that are not part of the asserted sequence.
	metrics.EXPECT().
		Counter(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(counter).
		AnyTimes()
	metrics.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).AnyTimes()
	hist.EXPECT().Observe(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().End().AnyTimes()
	logger.EXPECT().Info(gomock.Any(), gomock.Any()).AnyTimes()

	// The canonical runtime sequence on a stream-open failure: cb.Execute wraps
	// retrier.Retry wraps tracer.Start (on the way in); then the transport fails
	// so logger.Error (innermost) fires before counter.Inc (outermost metrics, on
	// the way back out). gomock.InOrder enforces this exact order across the five
	// mocks — a Build() reorder is caught at the offending call, and the
	// controller's cleanup asserts each fired exactly once.
	gomock.InOrder(
		cb.EXPECT().
			Execute(gomock.Any()).
			DoAndReturn(func(fn func() error) error { return fn() }),
		retrier.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, op func() error) error { return op() },
		),
		tracer.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
			func(
				ctx context.Context,
				_ string,
				_ ...interfaces.SpanOption,
			) (context.Context, interfaces.Span) {
				return ctx, span
			},
		),
		logger.EXPECT().Error(gomock.Any(), gomock.Any()),
		counter.EXPECT().Inc(gomock.Any()),
	)

	opts := grpcinterceptors.NewStreamingClientBuilder().
		WithTimeout(5 * time.Second).
		WithMetrics(metrics).
		WithCircuitBreaker(cb).
		WithRetry(retrier).
		WithTracing(tracer).
		WithLogging(logger).
		Build()

	conn, err := grpc.NewClient(
		addr,
		append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))...,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.NewStream(
		context.Background(),
		&grpc.StreamDesc{},
		"/test.Service/Method",
	)
	require.Error(t, err, "expected stream creation to fail against a closed listener")
	// gomock.InOrder + the controller's cleanup assert the canonical order:
	// metrics wraps circuit breaker wraps retry wraps tracing wraps logging wraps
	// the transport (cb → retry → tracing → logging → metrics at runtime).
}

// ---------------------------------------------------------------------------
// Cancellation without draining (client stream)
// ---------------------------------------------------------------------------
//
// Callers are expected to cancel a stream before it naturally finishes and
// walk away without ever calling RecvMsg again. The interceptors below must
// still record/end/log that outcome — they cannot rely solely on a terminal
// RecvMsg, since one may never come.

// TestGRPCMetricsStreamClientInterceptor_RecordsAbandonedCancellation tests
// that the client stream metrics interceptor still records a stream the
// caller cancels and never drains via RecvMsg.
//
// Why this test is important:
//   - If the interceptor only reacted to a terminal RecvMsg, a canceled
//     stream that the caller abandons without reading again would never be
//     counted, silently under-reporting cancellations/failures
//
// What it tests:
//   - Canceling the call context, without ever calling RecvMsg, still
//     records a Canceled-coded count via the background watch goroutine
func TestGRPCMetricsStreamClientInterceptor_RecordsAbandonedCancellation(t *testing.T) {
	t.Parallel()
	recorded := make(chan string, 1)
	interceptor := grpcinterceptors.MetricsStreamClientInterceptor(
		signalMetrics(gomock.NewController(t), recorded),
	)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	cancel() // caller abandons the stream without ever calling RecvMsg again

	select {
	case code := <-recorded:
		assert.Equal(t, codes.Canceled.String(), code)
	case <-time.After(2 * time.Second):
		t.Fatal(
			"metrics were not recorded after the caller canceled and abandoned the stream",
		)
	}
}

// TestGRPCTracingStreamClientInterceptor_EndsSpanOnAbandonedCancellation
// tests that the client stream tracing interceptor still ends the span for a
// stream the caller cancels and never drains via RecvMsg.
//
// Why this test is important:
//   - A span left open because the caller abandoned a canceled stream would
//     leak as a permanently "in flight" span in the tracing backend
//
// What it tests:
//   - Canceling the call context, without ever calling RecvMsg, still ends
//     the span via the background watch goroutine
func TestGRPCTracingStreamClientInterceptor_EndsSpanOnAbandonedCancellation(
	t *testing.T,
) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	ended := make(chan struct{})
	span, _ := signalSpan(ctrl, ended)
	interceptor := grpcinterceptors.TracingStreamClientInterceptor(
		signalTracer(ctrl, span),
	)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	cancel() // caller abandons the stream without ever calling RecvMsg again

	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("span was not ended after the caller canceled and abandoned the stream")
	}
}

// TestGRPCLoggingStreamClientInterceptor_LogsAbandonedCancellation tests that
// the client stream logging interceptor still logs a stream the caller
// cancels and never drains via RecvMsg.
//
// Why this test is important:
//   - A caller that cancels and abandons a stream should still leave an
//     audit trail; silently dropping that log line would hide real failures
//     from operators
//
// What it tests:
//   - Canceling the call context, without ever calling RecvMsg, still emits
//     an error log line via the background watch goroutine
func TestGRPCLoggingStreamClientInterceptor_LogsAbandonedCancellation(t *testing.T) {
	t.Parallel()
	logged := make(chan string, 1)
	interceptor := grpcinterceptors.LoggingStreamClientInterceptor(
		signalLogger(gomock.NewController(t), logged),
	)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	cancel() // caller abandons the stream without ever calling RecvMsg again

	select {
	case level := <-logged:
		assert.Equal(t, "error", level)
	case <-time.After(2 * time.Second):
		t.Fatal(
			"stream completion was not logged after the caller canceled and abandoned the stream",
		)
	}
}

// ---------------------------------------------------------------------------
// Drained-then-cancelled (client stream) — the RecvMsg-wins-the-race guarantee
// ---------------------------------------------------------------------------
//
// The inverse of the abandoned-cancellation tests above: the stream reaches a
// terminal RecvMsg (EOF) first, and only afterward does the caller cancel its
// context. finish's sync.Once must guarantee a single record no matter which
// of watch's two select cases actually runs, and the background watch
// goroutine must not leak once finish has run.

// TestGRPCMetricsStreamClientInterceptor_RecordsOnceWhenDrainedThenCancelled
// tests that a stream drained to EOF, then cancelled, still records exactly
// once (as OK) rather than double-recording or having watch's later ctx.Done
// overwrite the terminal RecvMsg's outcome.
//
// Why this test is important:
//   - finish is guarded by sync.Once specifically so a late-arriving watch
//     wakeup after a healthy RecvMsg can't relabel a successful RPC as
//     canceled/failed, or record it twice; this test locks that guarantee in
//   - It also proves the watch(ctx) goroutine launched per stream terminates
//     instead of leaking once the stream finishes, which goleak verifies
//
// What it tests:
//   - Draining a stream to io.EOF, then cancelling its context, records
//     exactly one "OK" count (no second record) and leaves no goroutine behind
func TestGRPCMetricsStreamClientInterceptor_RecordsOnceWhenDrainedThenCancelled(
	t *testing.T,
) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	recorded := make(chan string, 2)
	interceptor := grpcinterceptors.MetricsStreamClientInterceptor(
		signalMetrics(gomock.NewController(t), recorded),
	)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	var msg int
	require.ErrorIs(t, stream.RecvMsg(&msg), io.EOF, "expected the drained stream to EOF")

	cancel() // now abandon the already-finished stream

	select {
	case code := <-recorded:
		assert.Equal(t, "OK", code)
	case <-time.After(2 * time.Second):
		t.Fatal("metrics were not recorded after the stream drained to EOF")
	}

	select {
	case code := <-recorded:
		t.Fatalf("expected exactly one record, got a second one: %q", code)
	case <-time.After(100 * time.Millisecond):
		// no second record — as expected
	}
}

// TestGRPCTracingStreamClientInterceptor_DrainedThenCancelledEndsSpanOK tests
// that a stream drained to EOF, then cancelled, ends its span exactly once as
// OK rather than letting watch's later ctx.Done relabel the healthy span as
// canceled/deadline-exceeded.
//
// Why this test is important:
//   - This is the tracing analogue of the metrics single-record guarantee: a
//     span for a fully-drained stream must reflect success, and the per-stream
//     watch goroutine must terminate rather than leak
//   - It exercises watch's re-check of s.done after ctx.Done: the terminal
//     RecvMsg (EOF) is authoritative and its OK outcome must win
//
// What it tests:
//   - Draining to io.EOF, then cancelling, ends the span with SpanStatusOK and
//     leaves no goroutine behind
func TestGRPCTracingStreamClientInterceptor_DrainedThenCancelledEndsSpanOK(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	ctrl := gomock.NewController(t)
	ended := make(chan struct{})
	span, spanStatus := signalSpan(ctrl, ended)
	interceptor := grpcinterceptors.TracingStreamClientInterceptor(
		signalTracer(ctrl, span),
	)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	var msg int
	require.ErrorIs(t, stream.RecvMsg(&msg), io.EOF, "expected the drained stream to EOF")

	cancel() // now abandon the already-finished stream

	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("span was not ended after the stream drained to EOF")
	}
	assert.Equal(
		t,
		interfaces.SpanStatusOK,
		*spanStatus,
		"a healthy EOF stream must end its span OK, not Error, after a trailing cancel",
	)
}

// TestGRPCLoggingStreamClientInterceptor_DrainedThenCancelledLogsNoError tests
// that a stream drained to EOF, then cancelled, is not logged as an error —
// the healthy terminal RecvMsg wins over the trailing ctx cancellation.
//
// Why this test is important:
//   - The logging analogue of the metrics/tracing guarantee: a fully-drained
//     stream must not leave an error log line implying it failed/was canceled,
//     which would mislead operators and trip error-rate alerts
//   - It exercises watch's re-check of s.done after ctx.Done and confirms the
//     per-stream watch goroutine terminates rather than leaks
//
// What it tests:
//   - Draining to io.EOF, then cancelling, emits no error log and leaves no
//     goroutine behind (the success path logs via Info, which signalLogger
//     intentionally ignores)
func TestGRPCLoggingStreamClientInterceptor_DrainedThenCancelledLogsNoError(
	t *testing.T,
) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	logged := make(chan string, 1)
	interceptor := grpcinterceptors.LoggingStreamClientInterceptor(
		signalLogger(gomock.NewController(t), logged),
	)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := interceptor(
		ctx,
		&grpc.StreamDesc{},
		nil,
		"/test.Service/Method",
		func(
			ctx context.Context,
			_ *grpc.StreamDesc,
			_ *grpc.ClientConn,
			_ string,
			_ ...grpc.CallOption,
		) (grpc.ClientStream, error) {
			return fixtures.StubClientStream(ctx, nil), nil
		},
	)
	require.NoError(t, err, "unexpected error creating stream")

	var msg int
	require.ErrorIs(t, stream.RecvMsg(&msg), io.EOF, "expected the drained stream to EOF")

	cancel() // now abandon the already-finished stream

	select {
	case level := <-logged:
		t.Fatalf("a healthy EOF stream was logged as %q after a trailing cancel", level)
	case <-time.After(200 * time.Millisecond):
		// no error logged — as expected
	}
}
