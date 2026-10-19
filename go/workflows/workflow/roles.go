package workflow

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// resourceWorkflow is the workflow-tier interfaces.Resource over a resource's
// pipelines: each read runs as one operation with no transaction, and each write as one
// command in its own transaction.
type resourceWorkflow[T, P any, ID comparable, C, U any] struct {
	// pipes are the resource's pipelines.
	pipes interfaces.Resource[T, P, ID, C, U]

	// flows runs each operation and opens each command's transaction.
	flows Base
}

// Resource builds the resource workflow running pipes through b, whose contract embeds
// interfaces.Resource so each of its operations is declared.
func (b Base) Resource[T, P any, ID comparable, C, U any](
	pipes interfaces.Resource[T, P, ID, C, U],
) interfaces.Resource[T, P, ID, C, U] {
	return resourceWorkflow[T, P, ID, C, U]{flows: b, pipes: pipes}
}

// Get returns the entity identified by id, a read.
func (w resourceWorkflow[T, P, ID, C, U]) Get(ctx context.Context, id ID) (*T, error) {
	return w.flows.Exec(ctx, "get", func(ctx context.Context) (*T, error) {
		return w.pipes.Get(ctx, id)
	})
}

// List returns the page of entities params and page select, a read.
func (w resourceWorkflow[T, P, ID, C, U]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return w.flows.Exec(ctx, "list", func(ctx context.Context) (*types.Page[T], error) {
		return w.pipes.List(ctx, params, page)
	})
}

// Exists reports whether an entity identified by id exists, a read.
func (w resourceWorkflow[T, P, ID, C, U]) Exists(
	ctx context.Context,
	id ID,
) (bool, error) {
	return w.flows.Exec(ctx, "exists", func(ctx context.Context) (bool, error) {
		return w.pipes.Exists(ctx, id)
	})
}

// Create persists a new entity built from in, a command.
func (w resourceWorkflow[T, P, ID, C, U]) Create(ctx context.Context, in C) (*T, error) {
	return w.flows.ExecTx(ctx, "create", func(ctx context.Context) (*T, error) {
		return w.pipes.Create(ctx, in)
	})
}

// Update modifies the entity identified by id with in, a command.
func (w resourceWorkflow[T, P, ID, C, U]) Update(
	ctx context.Context,
	id ID,
	in U,
) (*T, error) {
	return w.flows.ExecTx(ctx, "update", func(ctx context.Context) (*T, error) {
		return w.pipes.Update(ctx, id, in)
	})
}

// Delete removes the entity identified by id, a command.
func (w resourceWorkflow[T, P, ID, C, U]) Delete(ctx context.Context, id ID) error {
	_, err := w.flows.ExecTx(ctx, "delete", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, w.pipes.Delete(ctx, id)
	})
	return err
}

// crudWorkflow is the workflow-tier interfaces.Service over a CRUD resource's
// pipelines: each read runs as one operation with no transaction, and each write as one
// command in its own transaction.
type crudWorkflow[T, P any, ID comparable] struct {
	// pipes are the resource's pipelines.
	pipes interfaces.Service[T, P, ID]

	// flows runs each operation and opens each command's transaction.
	flows Base
}

// CRUD builds the CRUD workflow running pipes through b, whose contract embeds
// interfaces.Service so each of its operations is declared.
func (b Base) CRUD[T, P any, ID comparable](
	pipes interfaces.Service[T, P, ID],
) interfaces.Service[T, P, ID] {
	return crudWorkflow[T, P, ID]{flows: b, pipes: pipes}
}

// Get returns the entity identified by id, a read.
func (w crudWorkflow[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return w.flows.Exec(ctx, "get", func(ctx context.Context) (*T, error) {
		return w.pipes.Get(ctx, id)
	})
}

// List returns the page of entities params and page select, a read.
func (w crudWorkflow[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return w.flows.Exec(ctx, "list", func(ctx context.Context) (*types.Page[T], error) {
		return w.pipes.List(ctx, params, page)
	})
}

// Create persists entity, a command.
func (w crudWorkflow[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return w.flows.ExecTx(ctx, "create", func(ctx context.Context) (*T, error) {
		return w.pipes.Create(ctx, entity)
	})
}

// Update replaces the entity identified by id with entity, a command.
func (w crudWorkflow[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	return w.flows.ExecTx(ctx, "update", func(ctx context.Context) (*T, error) {
		return w.pipes.Update(ctx, id, entity)
	})
}

// Delete removes the entity identified by id, a command.
func (w crudWorkflow[T, P, ID]) Delete(ctx context.Context, id ID) error {
	_, err := w.flows.ExecTx(ctx, "delete", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, w.pipes.Delete(ctx, id)
	})
	return err
}
