package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// NewBulkhead returns a middleware that runs the operation inside the bulkhead, so at
// most its limit of operations are in flight and the rest wait for a slot. It never
// rejects, so it reports nothing through its Observer (taken for a uniform layer
// signature).
func NewBulkhead(currentBulkhead interfaces.Bulkhead, _ Observer) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, _ string, next func(context.Context) error) error {
			return currentBulkhead.Execute(ctx, func() error { return next(ctx) })
		},
	)
}

// NewSheddingBulkhead returns a middleware that runs the operation inside the bulkhead
// and rejects it at once as CodeResourceExhausted when no slot is free (a server sheds
// load rather than queueing it). A shed operation is reported through obs.
func NewSheddingBulkhead(currentBulkhead interfaces.Bulkhead, obs Observer) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, operation string, next func(context.Context) error) error {
			ran, err := bulkhead.Shed(currentBulkhead, func() error { return next(ctx) })
			if !ran {
				obs.Recorder.Rejected(operation, series.ReasonBulkheadFull)

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
