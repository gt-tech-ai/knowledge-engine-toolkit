// Package budget tracks retry consumption across a call chain via context
// propagation.
//
// A Budget caps total retry attempts across a request lifecycle, preventing
// exponential retry amplification in deep call chains. Attach a budget to the
// context at the entrypoint, and each retrier in the chain consumes from it.
//
//	budget := budget.NewBudget(5)
//	ctx = budget.WithRetryBudget(ctx, budget)
//	// ... pass ctx through middleware, handlers, clients
package budget

import (
	"context"
	"sync/atomic"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// ErrRetryBudgetExhausted is returned when the retry budget is exhausted.
var ErrRetryBudgetExhausted = apperr.Sentinel("retry budget exhausted")

// budgetKey is the unexported context key under which a *Budget is stored, kept
// private so only this package can attach or retrieve the budget.
type budgetKey struct{}

// Budget tracks retry consumption across a call chain via context propagation.
type Budget struct {
	// remaining is the number of retries left, decremented atomically on each consumption.
	remaining atomic.Int32
}

// NewBudget creates a new retry budget with the given max retries.
func NewBudget(maxRetries int32) *Budget {
	b := &Budget{}
	b.remaining.Store(maxRetries)
	return b
}

// WithRetryBudget returns a context with the given retry budget attached.
func WithRetryBudget(ctx context.Context, b *Budget) context.Context {
	return context.WithValue(ctx, budgetKey{}, b)
}

// ConsumeRetry decrements the retry budget. Returns true if a retry is allowed.
// If no budget exists in context, retries are always allowed.
func ConsumeRetry(ctx context.Context) bool {
	b, ok := ctx.Value(budgetKey{}).(*Budget)
	if !ok {
		return true // no budget = unlimited
	}
	for {
		current := b.remaining.Load()
		if current <= 0 {
			return false
		}
		if b.remaining.CompareAndSwap(current, current-1) {
			return true
		}
	}
}

// Remaining returns the number of retries left in the budget.
// Returns -1 if no budget is attached to the context.
func Remaining(ctx context.Context) int32 {
	b, ok := ctx.Value(budgetKey{}).(*Budget)
	if !ok {
		return -1 // no budget
	}
	return b.remaining.Load()
}
