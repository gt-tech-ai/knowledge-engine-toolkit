package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// validationDecorator runs a business-rule validation function before the write
// operations (Create, Update) that carry an entity, short-circuiting on violation
// so invalid data never reaches the inner service. Reads (Get, List) and Delete
// carry no entity to validate and pass straight through.
type validationDecorator[T any, P any, ID comparable] struct {
	// inner is the next service in the decorator chain.
	inner interfaces.DecoratedService[T, P, ID]

	// validate returns a non-nil (coded) error when the entity violates a business rule.
	validate func(*T) error
}

// Get passes through — a read carries no entity to validate.
func (d *validationDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return d.inner.Get(ctx, id)
}

// List passes through — a read carries no entity to validate.
func (d *validationDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return d.inner.List(ctx, params, page)
}

// Create validates the entity first and short-circuits on violation, otherwise
// delegates to the inner service.
func (d *validationDecorator[T, P, ID]) Create(
	ctx context.Context,
	entity *T,
) (*T, error) {
	if err := d.validate(entity); err != nil {
		return nil, err
	}
	return d.inner.Create(ctx, entity)
}

// Update validates the entity first and short-circuits on violation, otherwise
// delegates to the inner service.
func (d *validationDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	if err := d.validate(entity); err != nil {
		return nil, err
	}
	return d.inner.Update(ctx, id, entity)
}

// Delete passes through — a delete carries no entity to validate.
func (d *validationDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	return d.inner.Delete(ctx, id)
}
