package repository

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// This file provides role-scoped decorated repositories — the counterpart of
// TxSafeInsertRepository for read-only and list-less resources. Each type embeds
// only the role interfaces its resource honors and decorates every method through
// the SAME unified op-decoration seam the custom-op path uses (opChain +
// decorate.Exec), so the six ordered cross-cutting bodies
// (ARCHITECTURE.md#decorator-order) live in exactly one place and are never
// re-implemented per role. Unlike the full-CRUD builder these deliberately omit the
// read-through caching decorator: caching is a passthrough for the op-decoration
// chain (see opChain), so a narrowed repository behaves like a decorated custom op —
// the resilience + observability stack, minus the transparent single-entity cache.

// ---------------------------------------------------------------------------
// Read-only (Reader + Lister + Exister)
// ---------------------------------------------------------------------------

// ReadListExistRepository is a resilience+observability-decorated read-only
// repository (Reader + Lister + Exister). Every operation runs through the shared
// opChain (tracing → metrics → logging → timeout → circuit-breaker → retry) via
// decorate.Exec, so a read-only resource gets the shared stack without embedding
// (and panicking) the write methods it does not support.
type ReadListExistRepository[T any, P any, ID comparable] struct {
	// store is the underlying read-only persistence seam.
	store interfaces.ReadListExistStore[T, P, ID]

	// chain decorates each read op (tracing/metrics/logging/timeout/cb/retry).
	chain decorate.OpMiddleware
}

// NewReadListExistRepository builds a decorated read-only repository over the
// store under the given name (stamped on metrics/logs/spans). Any resiliency
// collaborator may be nil (its middleware is skipped); a non-positive timeout
// skips the timeout middleware.
func NewReadListExistRepository[T, P any, ID comparable](
	name string,
	store interfaces.ReadListExistStore[T, P, ID],
	timeout time.Duration,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	retrier interfaces.Retrier,
	cb interfaces.CircuitBreaker,
) ReadListExistRepository[T, P, ID] {
	return ReadListExistRepository[T, P, ID]{
		store: store,
		chain: opChain(name, timeout, logger, metrics, tracer, retrier, cb),
	}
}

// Get retrieves an entity by id through the decorated read chain.
func (r ReadListExistRepository[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return decorate.Exec(ctx, r.chain, "Get",
		func(ctx context.Context) (*T, error) { return r.store.Get(ctx, id) })
}

// List retrieves a page of entities matching params through the decorated read chain.
func (r ReadListExistRepository[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return decorate.Exec(
		ctx,
		r.chain,
		"List",
		func(ctx context.Context) (*types.Page[T], error) {
			return r.store.List(ctx, params, page)
		},
	)
}

// Exists reports whether an entity with the given id exists, through the decorated read
// chain.
func (r ReadListExistRepository[T, P, ID]) Exists(
	ctx context.Context,
	id ID,
) (bool, error) {
	return decorate.Exec(ctx, r.chain, "Exists",
		func(ctx context.Context) (bool, error) { return r.store.Exists(ctx, id) })
}

// ---------------------------------------------------------------------------
// List-less full CRUD (Reader + Writer + Deleter + Exister, no Lister)
// ---------------------------------------------------------------------------

// CRUDNoListRepository is a resilience+observability-decorated repository for a
// list-less resource (Reader + Writer + Deleter + Exister). Get/Update/Delete/Exists
// run through the standard opChain (with retry); Create runs through a retry-free
// chain because a create is not idempotent — blindly retrying a create that
// actually landed post-commit would insert a duplicate, so, exactly like the
// full-CRUD retry decorator, Create is never retried. Circuit-breaker + timeout
// still guard Create; only retry is dropped from its chain.
type CRUDNoListRepository[T any, ID comparable] struct {
	// store is the underlying list-less persistence seam.
	store interfaces.CRUDNoListStore[T, ID]

	// chain decorates Get/Update/Delete/Exists (includes retry).
	chain decorate.OpMiddleware

	// createChain decorates Create WITHOUT retry (non-idempotent create).
	createChain decorate.OpMiddleware
}

// NewCRUDNoListRepository builds a decorated list-less repository over the store
// under the given name (stamped on metrics/logs/spans). Any resiliency
// collaborator may be nil (its middleware is skipped); a non-positive timeout
// skips the timeout middleware. Create is decorated by a retry-free chain (the
// retrier is applied to reads/update/delete/exists only).
func NewCRUDNoListRepository[T any, ID comparable](
	name string,
	store interfaces.CRUDNoListStore[T, ID],
	timeout time.Duration,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	retrier interfaces.Retrier,
	cb interfaces.CircuitBreaker,
) CRUDNoListRepository[T, ID] {
	return CRUDNoListRepository[T, ID]{
		store:       store,
		chain:       opChain(name, timeout, logger, metrics, tracer, retrier, cb),
		createChain: opChain(name, timeout, logger, metrics, tracer, nil, cb),
	}
}

// Get retrieves an entity by id through the decorated chain.
func (r CRUDNoListRepository[T, ID]) Get(ctx context.Context, id ID) (*T, error) {
	return decorate.Exec(ctx, r.chain, "Get",
		func(ctx context.Context) (*T, error) { return r.store.Get(ctx, id) })
}

// Create persists a new entity through the retry-free decorated chain.
func (r CRUDNoListRepository[T, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	return decorate.Exec(ctx, r.createChain, "Create",
		func(ctx context.Context) (*T, error) { return r.store.Create(ctx, entity) })
}

// Update modifies an existing entity through the decorated chain.
func (r CRUDNoListRepository[T, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	return decorate.Exec(ctx, r.chain, "Update",
		func(ctx context.Context) (*T, error) { return r.store.Update(ctx, id, entity) })
}

// Delete removes an entity by id through the decorated chain.
func (r CRUDNoListRepository[T, ID]) Delete(ctx context.Context, id ID) error {
	_, err := decorate.Exec(
		ctx,
		r.chain,
		"Delete",
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, r.store.Delete(ctx, id)
		},
	)
	return err
}

// Exists reports whether an entity with the given id exists, through the decorated chain.
func (r CRUDNoListRepository[T, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	return decorate.Exec(ctx, r.chain, "Exists",
		func(ctx context.Context) (bool, error) { return r.store.Exists(ctx, id) })
}
