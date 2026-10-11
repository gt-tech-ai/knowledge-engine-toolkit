package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// NewRetry returns a middleware that retries transient failures via the Retrier —
// EXCEPT inside a transaction, where it runs the operation exactly once. Retrying a
// statement inside the caller's transaction is unsafe (the transaction is already
// poisoned on the first error), so the tx-guard (interfaces.InTx) is preserved
// exactly as the former repository retry decorator enforced it.
func NewRetry(retrier interfaces.Retrier) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, _ string, next func(context.Context) error) error {
			if interfaces.InTx(ctx) {
				return next(ctx)
			}
			return retrier.Retry(ctx, func() error { return next(ctx) })
		},
	)
}
