package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// NewRateLimit returns a middleware that rejects an operation over the limiter's rate as
// CodeResourceExhausted, without running it, and reports the rejection through obs as
// rate_limited.
func NewRateLimit(l interfaces.RateLimiter, obs Observer) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, operation string, next func(context.Context) error) error {
			if !l.Allow() {
				obs.Recorder.Rejected(operation, series.ReasonRateLimited)

				return apperr.New(
					apperr.CodeResourceExhausted,
					operation+": rate limit exceeded",
				)
			}

			return next(ctx)
		},
	)
}
