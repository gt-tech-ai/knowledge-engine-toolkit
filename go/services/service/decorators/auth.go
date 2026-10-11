package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// authDecorator enforces authorization before each service operation.
// The authFn is called with the context and an action string; a non-nil
// return short-circuits the operation.
type authDecorator[T any, P any, ID comparable] struct {
	// inner is the next service in the decorator chain.
	inner interfaces.DecoratedService[T, P, ID]

	// authFn performs the authorization check. Returns nil to allow, error to deny.
	authFn func(ctx context.Context, action string) error

	// name is the service name used as the action prefix.
	name string
}

// Get authorizes the "<name>.Get" action, then delegates to the inner service.
func (d *authDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	if err := d.authFn(ctx, d.name+".Get"); err != nil {
		return nil, err
	}
	return d.inner.Get(ctx, id)
}

// List authorizes the "<name>.List" action, then delegates to the inner service.
func (d *authDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	if err := d.authFn(ctx, d.name+".List"); err != nil {
		return nil, err
	}
	return d.inner.List(ctx, params, page)
}

// Create authorizes the "<name>.Create" action, then delegates to the inner service.
func (d *authDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	if err := d.authFn(ctx, d.name+".Create"); err != nil {
		return nil, err
	}
	return d.inner.Create(ctx, entity)
}

// Update authorizes the "<name>.Update" action, then delegates to the inner service.
func (d *authDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	if err := d.authFn(ctx, d.name+".Update"); err != nil {
		return nil, err
	}
	return d.inner.Update(ctx, id, entity)
}

// Delete authorizes the "<name>.Delete" action, then delegates to the inner service.
func (d *authDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	if err := d.authFn(ctx, d.name+".Delete"); err != nil {
		return err
	}
	return d.inner.Delete(ctx, id)
}
