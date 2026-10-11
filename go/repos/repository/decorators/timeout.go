package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// timeoutDecorator wraps repository operations with a per-operation context
// deadline.
type timeoutDecorator[T any, P any, ID comparable] struct {
	// inner is the next repository in the decorator chain.
	inner interfaces.DecoratedRepository[T, P, ID]

	// timeout is the deadline applied to the context of each operation.
	timeout time.Duration
}

// Get applies the configured timeout to the context before delegating to the
// inner Get.
func (d *timeoutDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Get(ctx, id)
}

// List applies the configured timeout to the context before delegating to the
// inner List.
func (d *timeoutDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.List(ctx, params, page)
}

// Create applies the configured timeout to the context before delegating to
// the inner Create.
func (d *timeoutDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Create(ctx, entity)
}

// Update applies the configured timeout to the context before delegating to
// the inner Update.
func (d *timeoutDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Update(ctx, id, entity)
}

// Delete applies the configured timeout to the context before delegating to
// the inner Delete.
func (d *timeoutDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Delete(ctx, id)
}

// Exists applies the configured timeout to the context before delegating to
// the inner Exists.
func (d *timeoutDecorator[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Exists(ctx, id)
}
