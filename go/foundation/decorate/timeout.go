package decorate

import (
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// NewTimeout returns a middleware that bounds each operation with a per-op deadline and
// codes an overrun as CodeTimeout, so a deadline the layer set never surfaces as an
// uncoded context error. A non-positive duration is a passthrough.
func NewTimeout(timeout time.Duration) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, op string, next func(context.Context) error) error {
			if timeout <= 0 {
				return next(ctx)
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			err := next(ctx)
			if err != nil && apperr.StdIs(err, context.DeadlineExceeded) &&
				apperr.Code(err) != apperr.CodeTimeout {
				return apperr.Wrap(err, apperr.CodeTimeout, op+" timed out")
			}
			return err
		},
	)
}
