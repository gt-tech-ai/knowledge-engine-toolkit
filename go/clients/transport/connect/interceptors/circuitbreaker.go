package interceptors

import (
	"context"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/interceptorcore"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// CircuitBreakerInterceptor returns a client-side interceptor that wraps calls
// with a circuit breaker. When the circuit is open, requests fail immediately
// with CodeUnavailable. When closed or half-open, the inner handler is invoked
// normally.
func CircuitBreakerInterceptor(
	cb interfaces.CircuitBreaker,
) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			var finalResp connect.AnyResponse

			rejected, innerErr := interceptorcore.CircuitBreak(cb, func() error {
				resp, err := next(ctx, req)
				if err != nil {
					return err
				}
				finalResp = resp
				return nil
			})

			if rejected {
				// Circuit breaker rejected the call (circuit open).
				return nil, connect.NewError(
					connect.CodeUnavailable,
					apperr.New(apperr.CodeUnavailable, "circuit breaker open"),
				)
			}
			if innerErr != nil {
				return nil, innerErr
			}
			return finalResp, nil
		}
	}
}
