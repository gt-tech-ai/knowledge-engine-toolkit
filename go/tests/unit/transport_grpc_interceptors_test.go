package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/grpc/interceptors"
)

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

// TestGRPCTracingServerInterceptor_ContinuesCallerTrace tests that the gRPC server
// continues the trace its caller injected.
//
// Why this test is important:
//   - The gRPC client injects the W3C traceparent into the outgoing metadata; a server
//     that ignores it starts a new trace per hop, so one request fragments into one
//     disconnected trace per service.
//
// What it tests:
//   - A traceparent in the incoming metadata becomes the parent of the server span:
//     the handler's context carries the caller's trace id.
func TestGRPCTracingServerInterceptor_ContinuesCallerTrace(t *testing.T) {
	// NOT parallel: this test sets the global OTel propagator.
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(
		"traceparent", "00-"+traceID+"-00f067aa0ba902b7-01",
	))
	var got oteltrace.SpanContext
	_, err := grpcinterceptors.TracingServerInterceptor(fixtures.NopTracer())(
		ctx,
		nil,
		&grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"},
		func(ctx context.Context, _ any) (any, error) {
			got = oteltrace.SpanContextFromContext(ctx)
			return nil, nil //nolint:nilnil // the probe handler has no response
		},
	)
	require.NoError(t, err)
	assert.Equal(t, traceID, got.TraceID().String())
}
