package decorate

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
)

// NewBulkhead returns a middleware that runs the operation inside the bulkhead, so at
// most its limit of operations are in flight. The rest wait for a slot, or, when shed is
// set, are rejected at once as CodeResourceExhausted (a server sheds load rather than
// queueing it).
func NewBulkhead(b interfaces.Bulkhead, shed bool) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, op string, next func(context.Context) error) error {
			if !shed {
				return b.Execute(ctx, func() error { return next(ctx) })
			}
			ran, err := bulkhead.Shed(b, func() error { return next(ctx) })
			if !ran {
				return apperr.Wrap(
					err,
					apperr.CodeResourceExhausted,
					op+": concurrency limit reached",
				)
			}
			return err
		},
	)
}
