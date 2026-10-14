package interfaces

import "context"

// CQLBatchKind selects how a CQL batch is applied.
type CQLBatchKind int

const (
	// CQLLoggedBatch applies all statements or none (use within one partition only).
	CQLLoggedBatch CQLBatchKind = iota
	// CQLUnloggedBatch groups statements for one round-trip without the batch log.
	CQLUnloggedBatch
)

// CQLSession is a session on a CQL (Cassandra-protocol) database — self-managed
// Apache Cassandra or Amazon Keyspaces — limited to the dialect both share. Built by
// clients/cql; the stores over it are unit-tested with a generated mock.
type CQLSession interface {
	// Query prepares a statement with bound values.
	Query(stmt string, values ...any) CQLQuery
	// Batch starts an empty batch of the given kind.
	Batch(kind CQLBatchKind) CQLBatch
	// ExecuteBatch applies a batch built by this session's Batch.
	ExecuteBatch(b CQLBatch) error
	// Close closes the session's connections.
	Close()
}

// CQLQuery is a prepared statement awaiting execution.
type CQLQuery interface {
	// WithContext binds ctx (cancellation and deadline) to the execution.
	WithContext(ctx context.Context) CQLQuery
	// PageSize sets how many rows one page returns.
	PageSize(n int) CQLQuery
	// PageState resumes from a previous page; it also limits Iter to one page.
	PageState(state []byte) CQLQuery
	// Idempotent marks the statement safe to retry or speculatively execute.
	Idempotent(idempotent bool) CQLQuery
	// Exec runs a statement that returns no rows.
	Exec() error
	// Iter runs the statement and returns its row iterator.
	Iter() CQLIter
}

// CQLIter iterates a query's rows.
type CQLIter interface {
	// Scan reads the next row into dest; false at the end or on error.
	Scan(dest ...any) bool
	// PageState is the position after this page (empty when there are no more).
	PageState() []byte
	// Close ends the iteration and returns its error, coded.
	Close() error
}

// CQLBatch is a group of statements applied together.
type CQLBatch interface {
	// Query appends a statement with bound values.
	Query(stmt string, values ...any)
	// WithContext binds ctx to the batch execution.
	WithContext(ctx context.Context) CQLBatch
	// Size returns the number of statements.
	Size() int
}
