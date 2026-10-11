package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// RateLimitServerInterceptor returns a server-side interceptor that rejects
// requests when the rate limiter's token bucket is empty. Rejected requests
// receive a ResourceExhausted status code.
func RateLimitServerInterceptor(
	limiter interfaces.RateLimiter,
) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if !limiter.Allow() {
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}
