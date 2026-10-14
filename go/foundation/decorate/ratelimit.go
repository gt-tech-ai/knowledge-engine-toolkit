package decorate

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// NewRateLimit returns a middleware that rejects an operation over the limiter's rate as
// CodeResourceExhausted, without running it.
func NewRateLimit(l interfaces.RateLimiter) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, op string, next func(context.Context) error) error {
			if !l.Allow() {
				return apperr.New(
					apperr.CodeResourceExhausted,
					op+": rate limit exceeded",
				)
			}
			return next(ctx)
		},
	)
}
