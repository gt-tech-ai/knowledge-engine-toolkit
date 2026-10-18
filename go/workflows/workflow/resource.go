package workflow

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ResourceWorkflow is the workflow-tier Base of interfaces.Resource over a resource's
// pipelines: each read runs as one operation with no transaction, and each write as one
// command in its own transaction.
type ResourceWorkflow[T, P any, ID comparable, C, U any] struct {
	// pipes are the resource's pipelines.
	pipes interfaces.Resource[T, P, ID, C, U]

	// flows runs each operation and opens each command's transaction.
	flows Base
}

// NewResource builds the resource workflow running pipes through flows.
func NewResource[T, P any, ID comparable, C, U any](
	flows Base,
	pipes interfaces.Resource[T, P, ID, C, U],
) ResourceWorkflow[T, P, ID, C, U] {
	return ResourceWorkflow[T, P, ID, C, U]{flows: flows, pipes: pipes}
}

// Get returns the entity identified by id, a read.
func (w ResourceWorkflow[T, P, ID, C, U]) Get(ctx context.Context, id ID) (*T, error) {
	return w.flows.Exec(ctx, "Get", func(ctx context.Context) (*T, error) {
		return w.pipes.Get(ctx, id)
	})
}

// List returns the page of entities params and page select, a read.
func (w ResourceWorkflow[T, P, ID, C, U]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return w.flows.Exec(ctx, "List", func(ctx context.Context) (*types.Page[T], error) {
		return w.pipes.List(ctx, params, page)
	})
}

// Exists reports whether an entity identified by id exists, a read.
func (w ResourceWorkflow[T, P, ID, C, U]) Exists(
	ctx context.Context,
	id ID,
) (bool, error) {
	return w.flows.Exec(ctx, "Exists", func(ctx context.Context) (bool, error) {
		return w.pipes.Exists(ctx, id)
	})
}

// Create persists a new entity built from in, a command.
func (w ResourceWorkflow[T, P, ID, C, U]) Create(ctx context.Context, in C) (*T, error) {
	return w.flows.ExecTx(ctx, "Create", func(ctx context.Context) (*T, error) {
		return w.pipes.Create(ctx, in)
	})
}

// Update modifies the entity identified by id with in, a command.
func (w ResourceWorkflow[T, P, ID, C, U]) Update(
	ctx context.Context,
	id ID,
	in U,
) (*T, error) {
	return w.flows.ExecTx(ctx, "Update", func(ctx context.Context) (*T, error) {
		return w.pipes.Update(ctx, id, in)
	})
}

// Delete removes the entity identified by id, a command.
func (w ResourceWorkflow[T, P, ID, C, U]) Delete(ctx context.Context, id ID) error {
	_, err := w.flows.ExecTx(ctx, "Delete", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, w.pipes.Delete(ctx, id)
	})
	return err
}
