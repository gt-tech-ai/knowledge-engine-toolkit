package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// circuitBreakerDecorator wraps repository operations with a circuit breaker
// that fails fast when the circuit is open.
type circuitBreakerDecorator[T any, P any, ID comparable] struct {
	// inner is the next repository in the decorator chain.
	inner interfaces.DecoratedRepository[T, P, ID]

	// cb is the circuit breaker guarding every operation against the inner repository.
	cb interfaces.CircuitBreaker
}

// Get runs the inner Get through the circuit breaker, failing fast when the
// circuit is open.
func (d *circuitBreakerDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	var result *T
	err := d.cb.Execute(func() error {
		var e error
		result, e = d.inner.Get(ctx, id)
		return e
	})
	return result, err
}

// List runs the inner List through the circuit breaker, failing fast when the
// circuit is open.
func (d *circuitBreakerDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	var result *types.Page[T]
	err := d.cb.Execute(func() error {
		var e error
		result, e = d.inner.List(ctx, params, page)
		return e
	})
	return result, err
}

// Create runs the inner Create through the circuit breaker, failing fast when
// the circuit is open.
func (d *circuitBreakerDecorator[T, P, ID]) Create(
	ctx context.Context,
	entity *T,
) (*T, error) {
	var result *T
	err := d.cb.Execute(func() error {
		var e error
		result, e = d.inner.Create(ctx, entity)
		return e
	})
	return result, err
}

// Update runs the inner Update through the circuit breaker, failing fast when
// the circuit is open.
func (d *circuitBreakerDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	var result *T
	err := d.cb.Execute(func() error {
		var e error
		result, e = d.inner.Update(ctx, id, entity)
		return e
	})
	return result, err
}

// Delete runs the inner Delete through the circuit breaker, failing fast when
// the circuit is open.
func (d *circuitBreakerDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	return d.cb.Execute(func() error {
		return d.inner.Delete(ctx, id)
	})
}

// Exists runs the inner Exists through the circuit breaker, failing fast when
// the circuit is open.
func (d *circuitBreakerDecorator[T, P, ID]) Exists(
	ctx context.Context,
	id ID,
) (bool, error) {
	var result bool
	err := d.cb.Execute(func() error {
		var e error
		result, e = d.inner.Exists(ctx, id)
		return e
	})
	return result, err
}
