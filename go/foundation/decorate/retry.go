package decorate

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// NewRetry returns a middleware that retries transient failures via the Retrier —
// EXCEPT inside a transaction, where it runs the operation exactly once. Retrying a
// statement inside the caller's transaction is unsafe (the transaction is already
// poisoned on the first error), so the tx-guard (interfaces.InTx) is preserved
// exactly as the former repository retry decorator enforced it.
//
// Outside a transaction the layer reports through obs once the Retrier returns: each
// attempt after the first as a retry; a retry the retry budget refused as a
// budget_exhausted rejection; and an operation that still failed after at least one
// retry, for any other reason, as exhausted, with one Warn. A budget refusal is only a
// rejection, so no operation is counted twice. The layer sees how many attempts ran,
// not when each retry happened.
func NewRetry(retrier interfaces.Retrier, obs Observer) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, operation string, next func(context.Context) error) error {
			if interfaces.InTx(ctx) {
				return next(ctx)
			}

			attempts := 0
			err := retrier.Retry(ctx, func() error {
				attempts++

				return next(ctx)
			})

			recordRetries(&obs, operation, attempts, err)

			return err
		},
	)
}

// recordRetries reports one retried operation through obs: attempts-1 retries, then a
// budget refusal as a rejection or a failure after retrying as exhausted with a Warn.
func recordRetries(obs *Observer, operation string, attempts int, err error) {
	for range attempts - 1 {
		obs.Recorder.Retried(operation)
	}

	if err == nil {
		return
	}

	if apperr.StdIs(err, budget.ErrRetryBudgetExhausted) {
		obs.Recorder.Rejected(operation, series.ReasonBudgetExhausted)

		return
	}

	if attempts > 1 {
		obs.Recorder.RetryExhausted(operation)
		obs.Warn("retries exhausted", "operation", operation, "attempts", attempts)
	}
}
