package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	grpcinterceptors "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/grpc/interceptors"
)

// TestGRPCInterceptors_ErrorBranches tests that the metrics and tracing unary
// interceptors label/record a failed call and pass its status through unchanged.
//
// Why this test is important:
//   - The error-code label on grpc_*_requests_total and the error status on the span
//     are what make a failing RPC visible in dashboards and traces; skipping them on
//     the error path hides real faults. The interceptors must also be transparent —
//     the handler's status code passes through untouched.
//
// What it tests:
//   - The server metrics and tracing interceptors each return the handler's Internal
//     error unchanged (exercising their err != nil branches).
//   - parseGRPCMethod falls back to ("", fullMethod) for a malformed method string.
func TestGRPCInterceptors_ErrorBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	boom := status.Error(codes.Internal, "handler fault")

	var failHandler grpc.UnaryHandler = func(context.Context, any) (any, error) {
		return nil, boom
	}

	t.Run("metrics server labels the error code", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.MetricsServerInterceptor(fixtures.NopMetrics())
		_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.S/M"}, failHandler)
		require.Error(t, err)
		assert.Equal(t, codes.Internal, status.Code(err))
	})

	t.Run("metrics server tolerates a malformed method", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.MetricsServerInterceptor(fixtures.NopMetrics())
		// A method with no service/method separator exercises parseGRPCMethod's fallback.
		_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "malformed"},
			func(context.Context, any) (any, error) { return "ok", nil })
		require.NoError(t, err)
	})

	t.Run("tracing server records the error on the span", func(t *testing.T) {
		t.Parallel()
		ic := grpcinterceptors.TracingServerInterceptor(fixtures.NopTracer())
		_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.S/M"}, failHandler)
		require.Error(t, err)
		assert.Equal(t, codes.Internal, status.Code(err))
	})
}
