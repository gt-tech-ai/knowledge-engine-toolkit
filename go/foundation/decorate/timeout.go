package decorate

import (
	"context"
	"time"
)

// NewTimeout returns a middleware that bounds each operation with a per-op deadline.
// A non-positive duration is a passthrough (no deadline), matching the builders' rule
// of only adding the timeout decorator when timeout > 0.
func NewTimeout(timeout time.Duration) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, _ string, next func(context.Context) error) error {
			if timeout <= 0 {
				return next(ctx)
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return next(ctx)
		},
	)
}
