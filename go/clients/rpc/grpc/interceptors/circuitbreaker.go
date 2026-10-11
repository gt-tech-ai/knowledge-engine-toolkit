package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// CircuitBreakerClientInterceptor returns a client-side interceptor that wraps
// calls with a circuit breaker. When the circuit is open, requests fail
// immediately with codes.Unavailable. When closed or half-open, the invoker
// is called normally.
func CircuitBreakerClientInterceptor(
	cb interfaces.CircuitBreaker,
) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		rejected, innerErr := interceptorcore.CircuitBreak(cb, func() error {
			return invoker(ctx, method, req, reply, cc, opts...)
		})
		if rejected {
			// Circuit breaker rejected the call (circuit open).
			return status.Errorf(codes.Unavailable, "circuit breaker open")
		}
		return innerErr
	}
}

// CircuitBreakerStreamClientInterceptor returns a client-side stream
// interceptor that wraps stream creation with a circuit breaker. When the
// circuit is open, stream creation fails immediately with codes.Unavailable
// without invoking streamer. Only stream creation is gated — once a stream is
// established, send/recv errors are not routed through the breaker.
func CircuitBreakerStreamClientInterceptor(
	cb interfaces.CircuitBreaker,
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

		rejected, innerErr := interceptorcore.CircuitBreak(cb, func() error {
			s, err := streamer(ctx, desc, cc, method, opts...)
			if err != nil {
				return err
			}
			stream = s
			return nil
		})

		if rejected {
			return nil, status.Errorf(codes.Unavailable, "circuit breaker open")
		}
		if innerErr != nil {
			return nil, innerErr
		}
		return stream, nil
	}
}
