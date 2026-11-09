package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
)

// Shed names the shed option in a signature.
type Shed bool

// NewBulkhead returns a middleware that runs the operation inside the bulkhead, so at
// most its limit of operations are in flight. The rest wait for a slot, or, when shed is
// set, are rejected at once as CodeResourceExhausted (a server sheds load rather than
// queueing it).
func NewBulkhead(bulkheadValue interfaces.Bulkhead, shed Shed) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, operation string, next func(context.Context) error) error {
			if !shed {
				return bulkheadValue.Execute(ctx, func() error { return next(ctx) })
			}

			ran, err := bulkhead.Shed(bulkheadValue, func() error { return next(ctx) })
			if !ran {
				return apperr.Wrap(
					err,
					apperr.CodeResourceExhausted,
					operation+": concurrency limit reached",
				)
			}

			return err
		},
	)
}
