package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// retryDecorator wraps repository operations with automatic retry on
// transient failures using the configured Retrier.
//
// Retry is skipped when the operation runs inside a transaction
// (interfaces.InTx): once any statement fails, PostgreSQL aborts the whole
// transaction, so retrying the same statement only yields SQLSTATE 25P02 and
// masks the real error. Inside a transaction, the operation runs once and its
// error propagates so the transaction can be rolled back and retried as a whole
// at a higher level.
type retryDecorator[T any, P any, ID comparable] struct {
	// inner is the next repository in the decorator chain.
	inner interfaces.DecoratedRepository[T, P, ID]

	// retrier is the strategy that re-runs a failed operation on transient errors.
	retrier interfaces.Retrier
}

// Get retries the inner Get on transient failures per the Retrier strategy,
// unless inside a transaction (then it runs once).
func (d *retryDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	if interfaces.InTx(ctx) {
		return d.inner.Get(ctx, id)
	}
	var result *T
	err := d.retrier.Retry(ctx, func() error {
		var e error
		result, e = d.inner.Get(ctx, id)
		return e
	})
	return result, err
}

// List retries the inner List on transient failures per the Retrier strategy,
// unless inside a transaction (then it runs once).
func (d *retryDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	if interfaces.InTx(ctx) {
		return d.inner.List(ctx, params, page)
	}
	var result *types.Page[T]
	err := d.retrier.Retry(ctx, func() error {
		var e error
		result, e = d.inner.List(ctx, params, page)
		return e
	})
	return result, err
}

// Create runs the inner Create exactly once and never retries it: a create
// is not idempotent, so retrying after a transient error that actually landed
// post-commit (the write succeeded but the ack was lost) would insert a duplicate.
// Recovering a failed create must use an idempotency key / ON CONFLICT at a higher
// layer, not a blind retry here — so unlike the read-only Get/List, Create passes
// straight through to the inner repository.
func (d *retryDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return d.inner.Create(ctx, entity)
}

// Update retries the inner Update on transient failures per the Retrier
// strategy, unless inside a transaction (then it runs once).
func (d *retryDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	if interfaces.InTx(ctx) {
		return d.inner.Update(ctx, id, entity)
	}
	var result *T
	err := d.retrier.Retry(ctx, func() error {
		var e error
		result, e = d.inner.Update(ctx, id, entity)
		return e
	})
	return result, err
}

// Delete retries the inner Delete on transient failures per the Retrier
// strategy, unless inside a transaction (then it runs once).
func (d *retryDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	if interfaces.InTx(ctx) {
		return d.inner.Delete(ctx, id)
	}
	return d.retrier.Retry(ctx, func() error {
		return d.inner.Delete(ctx, id)
	})
}

// Exists retries the inner Exists on transient failures per the Retrier
// strategy, unless inside a transaction (then it runs once).
func (d *retryDecorator[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	if interfaces.InTx(ctx) {
		return d.inner.Exists(ctx, id)
	}
	var result bool
	err := d.retrier.Retry(ctx, func() error {
		var e error
		result, e = d.inner.Exists(ctx, id)
		return e
	})
	return result, err
}
