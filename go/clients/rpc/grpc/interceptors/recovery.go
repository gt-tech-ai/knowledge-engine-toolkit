// Package interceptors provides gRPC unary interceptors for cross-cutting
// concerns including observability, resilience, and request lifecycle management.
package interceptors

import (
	"context"
	"fmt"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// RecoveryServerInterceptor returns a server-side interceptor that recovers
// from panics in downstream handlers. The panic value and stack trace are
// logged via the provided logger and an Internal status is returned.
func RecoveryServerInterceptor(logger interfaces.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				logger.WithContext(ctx).Error(
					"panic recovered in gRPC handler",
					"panic", fmt.Sprintf("%v", r),
					"stack", string(stack),
					"method", info.FullMethod,
				)
				resp = nil
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}
