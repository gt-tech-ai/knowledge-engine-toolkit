package interceptors

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// LoggingServerInterceptor returns a server-side interceptor that logs the
// method, duration, and error (if any) for each RPC.
func LoggingServerInterceptor(logger interfaces.Logger) grpc.UnaryServerInterceptor {
	return unary(logging(logger))
}

// LoggingStreamServerInterceptor is LoggingServerInterceptor for streaming RPCs: one
// line when the stream ends.
func LoggingStreamServerInterceptor(
	logger interfaces.Logger,
) grpc.StreamServerInterceptor {
	return stream(logging(logger))
}

// logging writes one access line per RPC: info when it succeeds, error when it fails.
func logging(logger interfaces.Logger) around {
	return func(
		ctx context.Context, fullMethod string, next func(context.Context) error,
	) error {
		start := time.Now()
		err := next(ctx)
		duration := time.Since(start)
		if err != nil {
			logger.WithContext(ctx).Error(
				"gRPC request failed",
				"method", fullMethod,
				"duration", duration,
				"code", status.Code(err).String(),
				"error", err.Error(),
			)
		} else {
			logger.WithContext(ctx).Info(
				"gRPC request completed",
				"method", fullMethod,
				"duration", duration,
				"code", "OK",
			)
		}
		return err
	}
}
