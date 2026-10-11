package service

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// This file provides role-scoped decorated services — the service-tier
// counterpart of the role-scoped repositories. Each type
// embeds only the role interfaces its service honors and decorates every method
// through the SAME unified op-decoration seam the custom-op path uses
// (service.OpChain + decorate.Exec), so the ordered cross-cutting
// bodies (recovery → tracing → metrics → logging → auth → timeout) live in
// exactly one place and are never re-implemented per role.

// CRUDNoListRepo is the list-less repository surface a list-less service
// delegates to: Get + Create + Update + Delete (services expose no Exists). A
// list-less repository (e.g. repository.CRUDNoListRepository:
// Reader+Writer+Deleter+Exister) satisfies it.
type CRUDNoListRepo[T any, ID comparable] interface {
	// Reader contributes Get (read by id).
	interfaces.Reader[T, ID]
	// Writer contributes Create and Update.
	interfaces.Writer[T, ID]
	// Deleter contributes Delete (remove by id).
	interfaces.Deleter[ID]
}

// CRUDNoListService is a decorated domain service for a list-less resource
// (Reader + Writer + Deleter, no Lister). It delegates each op to the repository
// through the shared service OpChain, so it carries the shared
// recovery/observability/timeout stack without embedding (and panicking) a List
// the resource cannot serve. It is the service-tier sibling of the full-CRUD
// decorator builder for resources whose collection is not listable (e.g. one
// addressed only by id or an external id).
type CRUDNoListService[T any, ID comparable] struct {
	// repo is the underlying list-less repository.
	repo CRUDNoListRepo[T, ID]

	// chain decorates every op (recovery/tracing/metrics/logging/auth/timeout).
	chain decorate.OpMiddleware
}

// NewCRUDNoListService builds a decorated list-less service over the repository
// under the given name (stamped on metrics/logs/spans, and used as the
// "<name>.<Op>" authorization action prefix). Any nil observability collaborator
// (or nil authFn / non-positive timeout) skips its middleware; recovery is always
// present (the service OpChain always adds it).
func NewCRUDNoListService[T any, ID comparable](
	name string,
	repo CRUDNoListRepo[T, ID],
	timeout time.Duration,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	authFn func(ctx context.Context, action string) error,
) CRUDNoListService[T, ID] {
	return CRUDNoListService[T, ID]{
		repo:  repo,
		chain: OpChain(name, timeout, logger, metrics, tracer, authFn),
	}
}

// Get retrieves an entity by id through the decorated service chain.
func (s CRUDNoListService[T, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return decorate.Exec(ctx, s.chain, "Get",
		func(ctx context.Context) (*T, error) { return s.repo.Get(ctx, id) })
}

// Create persists a new entity through the decorated service chain.
func (s CRUDNoListService[T, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return decorate.Exec(ctx, s.chain, "Create",
		func(ctx context.Context) (*T, error) { return s.repo.Create(ctx, entity) })
}

// Update modifies an existing entity through the decorated service chain.
func (s CRUDNoListService[T, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	return decorate.Exec(ctx, s.chain, "Update",
		func(ctx context.Context) (*T, error) { return s.repo.Update(ctx, id, entity) })
}

// Delete removes an entity by id through the decorated service chain.
func (s CRUDNoListService[T, ID]) Delete(ctx context.Context, id ID) error {
	_, err := decorate.Exec(
		ctx,
		s.chain,
		"Delete",
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, s.repo.Delete(ctx, id)
		},
	)
	return err
}
