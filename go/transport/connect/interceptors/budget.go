package interceptors

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/budget"
)

// DefaultRetryBudget is the per-request retry cap servers attach at the
// entrypoint: a single logical request may spend at most this many retries
// across every retrier in its chain (DB repo retriers + outbound client
// retriers), so one request can never fan out into unbounded retries.
const DefaultRetryBudget int32 = 10

// BudgetInterceptor attaches a fresh retry budget to each inbound unary request's
// context at the server entrypoint. Every retrier in the downstream chain — the
// handler's repo/DB retriers and any outbound client retriers — then draws from this
// one shared per-request cap instead of each multiplying retries independently. A
// streaming RPC passes through without a budget, as before. maxRetries <= 0 disables it
// (retries are then bounded only per-call by each retrier's own MaxRetries).
func BudgetInterceptor(maxRetries int32) connect.ServerInterceptor {
	return contextInterceptor(
		func(ctx context.Context, spec connect.Spec, _ *connect.Header) (
			context.Context, error,
		) {
			if maxRetries > 0 && spec.StreamType == connect.StreamTypeUnary {
				ctx = budget.WithRetryBudget(ctx, budget.NewBudget(maxRetries))
			}

			return ctx, nil
		},
	)
}
