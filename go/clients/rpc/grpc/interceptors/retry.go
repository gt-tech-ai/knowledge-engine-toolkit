package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// isNonRetryableGRPC reports whether err carries a gRPC status code that must
// not be retried — the framework-specific classifier the interceptorcore retry
// loop calls. The non-retryable codes are permanent client/domain failures, plus
// ResourceExhausted — retrying a rate-limited/quota-exceeded call immediately
// only adds load and deepens the backpressure. A parent-context deadline/cancel
// is handled by the retrier's context-awareness (it stops when ctx is done), not
// classified here, so a per-attempt (child-context) timeout can still be retried.
func isNonRetryableGRPC(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument,
		codes.NotFound,
		codes.AlreadyExists,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.Unimplemented,
		codes.Canceled,
		codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

// RetryClientInterceptor returns a client-side interceptor that retries
// transient failures using the provided Retrier. Non-retryable status codes
// (e.g. InvalidArgument, NotFound) are returned immediately without retry.
func RetryClientInterceptor(retrier interfaces.Retrier) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		permanent, exhausted := interceptorcore.Retry(
			ctx,
			retrier,
			isNonRetryableGRPC,
			func() error {
				return invoker(ctx, method, req, reply, cc, opts...)
			},
		)
		if permanent != nil {
			return permanent
		}
		if exhausted != nil {
			return status.Errorf(codes.Unavailable, "retries exhausted: %v", exhausted)
		}
		return nil
	}
}

// RetryStreamClientInterceptor returns a client-side stream interceptor that
// retries stream creation on transient failures using the provided Retrier.
// Only the initial streamer call is retried — once a stream is established,
// retrying a send/recv failure would require replaying application state the
// interceptor has no visibility into, so it is left to the caller.
// Non-retryable status codes (e.g. InvalidArgument, NotFound) fail
// immediately without retry.
func RetryStreamClientInterceptor(
	retrier interfaces.Retrier,
) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		var stream grpc.ClientStream

		permanent, exhausted := interceptorcore.Retry(
			ctx,
			retrier,
			isNonRetryableGRPC,
			func() error {
				s, err := streamer(ctx, desc, cc, method, opts...)
				if err != nil {
					return err
				}
				stream = s
				return nil
			},
		)

		if permanent != nil {
			return nil, permanent
		}
		if exhausted != nil {
			return nil, status.Errorf(
				codes.Unavailable,
				"retries exhausted: %v",
				exhausted,
			)
		}
		return stream, nil
	}
}
