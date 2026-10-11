// Package service provides BaseService with decorator support for building
// domain service layers with cross-cutting concerns.
//
// BaseService delegates all CRUD operations to an interfaces.Repository,
// keeping the service layer thin. Use the decorators sub-package to wrap
// services with logging, authorization, and panic recovery via a fluent builder.
package service

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Ensure BaseService implements the core Service interface.
var _ interfaces.Service[any, any, string] = (*BaseService[any, any, string])(nil)

// BaseService wraps a Repository and implements the core Service interface.
// It is the standard entry point for building decorated service stacks.
type BaseService[T any, P any, ID comparable] struct {
	// repo is the underlying repository for persistence operations.
	repo interfaces.Repository[T, P, ID]

	// name is the service name used as a label in logs and authorization actions.
	name string
}

// New creates a new BaseService wrapping the given repository.
func New[T, P any, ID comparable](
	repo interfaces.Repository[T, P, ID],
	name string,
) *BaseService[T, P, ID] {
	return &BaseService[T, P, ID]{repo: repo, name: name}
}

// Name returns the service name (used for decorator labels).
func (s *BaseService[T, P, ID]) Name() string { return s.name }

// Get retrieves an entity by its identifier.
func (s *BaseService[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return s.repo.Get(ctx, id)
}

// List retrieves a paginated list of entities matching the given filter parameters.
func (s *BaseService[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return s.repo.List(ctx, params, page)
}

// Create persists a new entity and returns the created record.
func (s *BaseService[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return s.repo.Create(ctx, entity)
}

// Update modifies an existing entity identified by id and returns the updated record.
func (s *BaseService[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	return s.repo.Update(ctx, id, entity)
}

// Delete removes an entity by its identifier.
func (s *BaseService[T, P, ID]) Delete(ctx context.Context, id ID) error {
	return s.repo.Delete(ctx, id)
}
