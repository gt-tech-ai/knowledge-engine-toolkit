package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// timeoutDecorator wraps service operations with a per-operation context
// deadline.
type timeoutDecorator[T any, P any, ID comparable] struct {
	// inner is the next service in the decorator chain.
	inner interfaces.DecoratedService[T, P, ID]

	// timeout is the deadline applied to each operation's context.
	timeout time.Duration
}

// Get delegates to the inner service under a context bounded by the timeout.
func (d *timeoutDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Get(ctx, id)
}

// List delegates to the inner service under a context bounded by the timeout.
func (d *timeoutDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.List(ctx, params, page)
}

// Create delegates to the inner service under a context bounded by the timeout.
func (d *timeoutDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Create(ctx, entity)
}

// Update delegates to the inner service under a context bounded by the timeout.
func (d *timeoutDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Update(ctx, id, entity)
}

// Delete delegates to the inner service under a context bounded by the timeout.
func (d *timeoutDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Delete(ctx, id)
}
