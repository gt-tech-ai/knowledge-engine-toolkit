package interceptors

import (
	"context"

	"connectrpc.com/connect"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// rateLimitInterceptor rejects requests — unary and server-streaming — when the rate
// limiter's token bucket is empty, shedding with ResourceExhausted. Server-side only
// (WrapStreamingClient is a no-op).
type rateLimitInterceptor struct {
	// limiter is the token-bucket rate limiter consulted per request.
	limiter interfaces.RateLimiter
}

// RateLimitInterceptor returns a server-side interceptor that rejects requests (unary
// and streaming) when the rate limiter's token bucket is empty. Rejected requests
// receive a ResourceExhausted error code.
func RateLimitInterceptor(limiter interfaces.RateLimiter) connect.Interceptor {
	return rateLimitInterceptor{limiter: limiter}
}

// rejected is the load-shed error returned when the limiter denies a call.
func (i rateLimitInterceptor) rejected() error {
	return connect.NewError(
		connect.CodeResourceExhausted,
		apperr.New(apperr.CodeUnavailable, "rate limit exceeded"),
	)
}

// WrapUnary sheds a unary request when the limiter denies it.
func (i rateLimitInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !i.limiter.Allow() {
			return nil, i.rejected()
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side load-shedding interceptor.
func (i rateLimitInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler sheds a server-streaming request when the limiter denies it,
// before the handler runs.
func (i rateLimitInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if !i.limiter.Allow() {
			return i.rejected()
		}
		return next(ctx, conn)
	}
}
