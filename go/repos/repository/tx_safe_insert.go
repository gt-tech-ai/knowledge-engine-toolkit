package repository

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// InsertOnlyStore is the minimal insert-only, tx-aware persistence seam the
// tx-safe repositories wrap — a store whose Insert honors a transaction on the
// context. It is the append-only write role (embeds interfaces.Inserter[T]).
type InsertOnlyStore[T any] interface {
	// Inserter contributes Insert (tx-aware append-only write).
	interfaces.Inserter[T]
}

// TxSafeInsertRepository is a tx-safe, observability-decorated
// (tracing+metrics+logging) pass-through for an insert-only store. It
// deliberately OMITS retry/circuit-breaker/timeout: its Insert runs inside the
// caller's transaction, where retrying or wrapping a timeout context would
// corrupt the transaction. Embed it in a repository (e.g. a
// transactional-outbox or audit repository) so the single tx-safe-insert
// behavior lives in one place (rather than being copied per repository). This
// is the platform-shared form of the pattern (used by any service writing an
// outbox/audit row inside a caller transaction).
type TxSafeInsertRepository[T any] struct {
	// store is the underlying tx-aware persistence seam.
	store InsertOnlyStore[T]

	// chain decorates Insert with tracing+metrics+logging (no
	// retry/cb/timeout).
	chain decorate.OpMiddleware
}

// NewTxSafeInsertRepository builds the shared tx-safe insert repository under
// the given repository name (stamped on the custom-op metrics, logs, and
// spans). The logger, metrics, and tracer may be nil (their middleware is
// skipped). The op chain is built with a zero timeout and nil retrier/breaker
// so the decoration adds no retry/cb/timeout — the tx-safe guarantee.
func NewTxSafeInsertRepository[T any](
	name string,
	store InsertOnlyStore[T],
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
) TxSafeInsertRepository[T] {
	return TxSafeInsertRepository[T]{
		store: store,
		chain: OpChain(name, 0, logger, metrics, tracer, nil, nil),
	}
}

// Insert persists one row within the caller's transaction, decorated with
// tracing+metrics+logging via decorate.Exec (no retry — see the type doc).
func (r TxSafeInsertRepository[T]) Insert(ctx context.Context, e *T) error {
	_, err := decorate.Exec(ctx, r.chain, "Insert",
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, r.store.Insert(ctx, e)
		})
	return err
}
