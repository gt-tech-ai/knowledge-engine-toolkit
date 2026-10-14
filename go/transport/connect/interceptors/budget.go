package interceptors

import (
	"context"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
)

// DefaultRetryBudget is the per-request retry cap servers attach at the
// entrypoint: a single logical request may spend at most this many retries
// across every retrier in its chain (DB repo retriers + outbound client
// retriers), so one request can never fan out into unbounded retries.
const DefaultRetryBudget int32 = 10

// BudgetInterceptor attaches a fresh retry budget to each inbound request's
// context at the server entrypoint. Every retrier in the downstream chain —
// the handler's repo/DB retriers and any outbound client retriers — then draws
// from this one shared per-request cap instead of each multiplying retries
// independently. maxRetries <= 0 disables it (retries are then bounded only
// per-call by each retrier's own MaxRetries).
func BudgetInterceptor(maxRetries int32) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if maxRetries > 0 {
				ctx = budget.WithRetryBudget(ctx, budget.NewBudget(maxRetries))
			}
			return next(ctx, req)
		}
	}
}
