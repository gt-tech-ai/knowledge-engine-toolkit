// Package interfaces defines the core contracts for the Tech AI Knowledge Engine.
// All services program against these interfaces. Implementations are provided
// by the foundation and platform layers.
package interfaces

import (
	"context"
	"sync"
)

// Repository is the generic data access interface, composed from the role
// interfaces — the same full-CRUD surface as before, now assembled
// from Reader/Lister/Writer/Deleter/Exister so a resource can depend on only the
// roles it uses.
// T = entity type, P = query/filter params, ID = identifier type.
type Repository[T any, P any, ID comparable] interface {
	// Reader provides Get (retrieve by id).
	Reader[T, ID]
	// Lister provides List (paginated query by params).
	Lister[T, P]
	// Writer provides Create and Update.
	Writer[T, ID]
	// Deleter provides Delete (remove by id).
	Deleter[ID]
	// Exister provides Exists (existence check by id).
	Exister[ID]
}

// DecoratedRepository is the fully-decorated CRUD surface of a repository (the base
// Repository methods wrapped by the resilience + observability decorator stack). It is
// the type the decorator builder returns and that concrete repositories embed. Custom
// (non-CRUD) methods no longer decorate through a method here — they run through the
// type-agnostic decorate.OpMiddleware chain via decorate.Exec[R], which lets
// them return any type without the former (*T,error) shoehorn.
type DecoratedRepository[T any, P any, ID comparable] interface {
	// Repository is the base CRUD surface the decorator stack wraps.
	Repository[T, P, ID]
}

// Transaction represents a database transaction.
type Transaction interface {
	// Commit commits the transaction.
	Commit() error

	// Rollback aborts the transaction.
	Rollback() error
}

// TransactionManager handles transaction lifecycle.
type TransactionManager interface {
	// Begin starts a new transaction and returns a context with the transaction attached.
	Begin(ctx context.Context) (context.Context, Transaction, error)

	// WithTransaction executes fn within a transaction, automatically committing
	// on success or rolling back on error.
	WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// inTxKey is the unexported context key marking an active transaction.
type inTxKey struct{}

// WithInTx returns a context marked as executing inside a transaction. A
// TransactionManager sets this when it opens a transaction so that generic
// infrastructure (e.g. the repository retry decorator) can detect it without
// depending on any concrete ORM/transaction implementation.
func WithInTx(ctx context.Context) context.Context {
	return context.WithValue(ctx, inTxKey{}, true)
}

// InTx reports whether ctx is executing inside a transaction opened by a
// TransactionManager. Statement-level retry must be skipped inside a
// transaction: once any statement fails the transaction is aborted and every
// subsequent command fails until it ends, so retrying only masks the real error.
func InTx(ctx context.Context) bool {
	v, _ := ctx.Value(inTxKey{}).(bool)
	return v
}

// afterCommitKey is the unexported context key holding the after-commit hook registry.
type afterCommitKey struct{}

// afterCommitReg collects hooks to run after a transaction commits successfully.
type afterCommitReg struct {
	// hooks are the post-commit callbacks queued in registration order.
	hooks []func()
	// mu guards concurrent appends to hooks.
	mu sync.Mutex
}

// WithAfterCommit returns a context carrying a fresh after-commit registry. A
// TransactionManager installs it when it opens a transaction, then calls
// RunAfterCommit once the commit succeeds. Code running inside the transaction
// schedules post-commit side effects via RegisterAfterCommit.
func WithAfterCommit(ctx context.Context) context.Context {
	return context.WithValue(ctx, afterCommitKey{}, &afterCommitReg{})
}

// RegisterAfterCommit schedules fn to run AFTER the enclosing transaction commits
// successfully. If ctx is not inside a transaction (no registry installed), fn runs
// immediately — so a non-transactional caller still sees the side effect. This lets a
// side effect that must not become visible until the write is durable — e.g. rotating a
// cache generation token, so a concurrent reader cannot cache a pre-commit snapshot under
// the new generation — be scheduled uniformly whether or not the write is wrapped in a
// transaction.
func RegisterAfterCommit(ctx context.Context, fn func()) {
	reg, ok := ctx.Value(afterCommitKey{}).(*afterCommitReg)
	if !ok || reg == nil {
		fn()
		return
	}
	reg.mu.Lock()
	reg.hooks = append(reg.hooks, fn)
	reg.mu.Unlock()
}

// RunAfterCommit runs every hook registered on ctx in registration order, then clears
// them. A TransactionManager calls this exactly once after a successful commit. It is a
// no-op when no registry is present.
func RunAfterCommit(ctx context.Context) {
	reg, ok := ctx.Value(afterCommitKey{}).(*afterCommitReg)
	if !ok || reg == nil {
		return
	}
	reg.mu.Lock()
	hooks := reg.hooks
	reg.hooks = nil
	reg.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}
