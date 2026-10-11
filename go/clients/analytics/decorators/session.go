package decorators

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cassandra"
	clientdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
)

// Session wraps a cassandra.Session so every statement execution (Query.Exec)
// and batch runs through the client stack (Bulkhead → Retry → CircuitBreaker →
// Timeout → Tracing → Metrics → Logging). Only statements marked Idempotent are
// retried — the store's writes are, so a transient failure is retried within the
// stack's retry budget. Reads (Iter) are paged by the caller and protected by the
// store decorators instead.
func Session(inner cassandra.Session, stack *clientdecorators.Stack) cassandra.Session {
	return &session{inner: inner, stack: stack}
}

// session is the decorated Session.
type session struct {
	// inner is the wrapped session.
	inner cassandra.Session
	// stack applies the client-boundary layers.
	stack *clientdecorators.Stack
}

// Query returns a decorated query.
func (s *session) Query(stmt string, values ...any) cassandra.Query {
	return &query{
		inner: s.inner.Query(stmt, values...),
		stack: s.stack,
		ctx:   context.Background(),
	}
}

// Batch returns a decorated batch.
func (s *session) Batch(kind cassandra.BatchKind) cassandra.Batch {
	return &batch{inner: s.inner.Batch(kind), ctx: context.Background()}
}

// ExecuteBatch runs the batch through the stack (retried: batches here are idempotent
// upserts and deletes).
func (s *session) ExecuteBatch(b cassandra.Batch) error {
	inner, ctx := b, context.Background()
	if db, ok := b.(*batch); ok {
		inner, ctx = db.inner, db.ctx
	}
	_, err := clientdecorators.Run(
		ctx,
		s.stack,
		"cassandra.batch",
		clientdecorators.RunOpts{Retryable: true},
		func(cctx context.Context) (struct{}, error) {
			return struct{}{}, s.inner.ExecuteBatch(inner.WithContext(cctx))
		},
	)
	return err
}

// Close closes the inner session.
func (s *session) Close() { s.inner.Close() }

// query is a decorated Query that remembers its context and idempotency.
type query struct {
	// ctx is the statement's context (set by WithContext).
	ctx context.Context //nolint:containedctx // mirrors gocql's builder
	// inner is the wrapped query.
	inner cassandra.Query
	// stack applies the client-boundary layers.
	stack *clientdecorators.Stack
	// idempotent reports the statement may be retried.
	idempotent bool
}

// WithContext binds ctx.
func (q *query) WithContext(ctx context.Context) cassandra.Query {
	q.ctx, q.inner = ctx, q.inner.WithContext(ctx)
	return q
}

// PageSize sets the page size.
func (q *query) PageSize(
	n int,
) cassandra.Query {
	q.inner = q.inner.PageSize(n)
	return q
}

// PageState sets the resume position.
func (q *query) PageState(
	state []byte,
) cassandra.Query {
	q.inner = q.inner.PageState(state)
	return q
}

// Idempotent marks the statement retryable.
func (q *query) Idempotent(idempotent bool) cassandra.Query {
	q.idempotent, q.inner = idempotent, q.inner.Idempotent(idempotent)
	return q
}

// Exec runs the statement through the stack (each attempt bound to the stack's
// per-attempt context), retried only when idempotent.
func (q *query) Exec() error {
	_, err := clientdecorators.Run(
		q.ctx,
		q.stack,
		"cassandra.exec",
		clientdecorators.RunOpts{Retryable: q.idempotent},
		func(cctx context.Context) (struct{}, error) {
			return struct{}{}, q.inner.WithContext(cctx).Exec()
		},
	)
	return err
}

// Iter returns the inner iterator.
func (q *query) Iter() cassandra.Iter { return q.inner.Iter() }

// batch is a decorated Batch that remembers its context.
type batch struct {
	// ctx is the batch's context.
	ctx context.Context //nolint:containedctx // mirrors gocql's builder
	// inner is the wrapped batch.
	inner cassandra.Batch
}

// Query appends a statement.
func (b *batch) Query(stmt string, values ...any) { b.inner.Query(stmt, values...) }

// WithContext binds ctx.
func (b *batch) WithContext(ctx context.Context) cassandra.Batch {
	b.ctx, b.inner = ctx, b.inner.WithContext(ctx)
	return b
}

// Size returns the number of statements.
func (b *batch) Size() int { return b.inner.Size() }
