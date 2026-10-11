// Package repository provides the BaseRepository implementation with decorator
// support and keyset pagination for building data access layers.
//
// BaseRepository delegates all persistence operations to an interfaces.Store,
// keeping the repository layer thin and focused on composition. Use the
// decorators sub-package to wrap repositories with logging, tracing, metrics,
// and caching via a fluent builder.
package repository

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Ensure BaseRepository implements the core Repository interface.
var _ interfaces.Repository[any, any, string] = (*BaseRepository[any, any, string])(nil)

// BaseRepository wraps a Store and implements the core Repository interface.
// It is the standard entry point for building decorated repository stacks.
type BaseRepository[T any, P any, ID comparable] struct {
	// store is the underlying persistence implementation.
	store interfaces.Store[T, P, ID]

	// name is the repository name used as a label in metrics, logs, and spans.
	name string
}

// New creates a new BaseRepository wrapping the given store.
func New[T, P any, ID comparable](
	store interfaces.Store[T, P, ID],
	name string,
) *BaseRepository[T, P, ID] {
	return &BaseRepository[T, P, ID]{store: store, name: name}
}

// Name returns the repository name (used for metrics/logging labels).
func (r *BaseRepository[T, P, ID]) Name() string {
	return r.name
}

// Get retrieves an entity by its identifier. Returns an error if the entity is not found.
func (r *BaseRepository[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return r.store.Get(ctx, id)
}

// List retrieves a paginated list of entities matching the given filter parameters.
func (r *BaseRepository[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return r.store.List(ctx, params, page)
}

// Create persists a new entity and returns the created record.
func (r *BaseRepository[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return r.store.Create(ctx, entity)
}

// Update modifies an existing entity identified by id and returns the updated
// record.
func (r *BaseRepository[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	return r.store.Update(ctx, id, entity)
}

// Delete removes an entity by its identifier.
func (r *BaseRepository[T, P, ID]) Delete(ctx context.Context, id ID) error {
	return r.store.Delete(ctx, id)
}

// Exists checks whether an entity with the given identifier exists.
func (r *BaseRepository[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	return r.store.Exists(ctx, id)
}
