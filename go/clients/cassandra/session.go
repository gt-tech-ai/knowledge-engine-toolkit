package cassandra

import (
	"context"
	"net"

	"github.com/gocql/gocql"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// BatchKind selects how a batch is applied.
type BatchKind int

const (
	// LoggedBatch applies all statements or none (use within one partition only).
	LoggedBatch BatchKind = iota
	// UnloggedBatch groups statements for one round-trip without the batch log.
	UnloggedBatch
)

// Session is the consumer-side seam over a gocql session, so stores are unit-tested
// with a generated mock.
//
// SDK seam: a thin projection of *gocql.Session; it embeds no core interface.
type Session interface {
	// Query prepares a statement with bound values.
	Query(stmt string, values ...any) Query
	// Batch starts an empty batch of the given kind.
	Batch(kind BatchKind) Batch
	// ExecuteBatch applies a batch built by this session's Batch.
	ExecuteBatch(b Batch) error
	// Close closes the session's connections.
	Close()
}

// Query is a prepared statement awaiting execution.
//
// SDK seam: a thin projection of *gocql.Query.
type Query interface {
	// WithContext binds ctx (cancellation and deadline) to the execution.
	WithContext(ctx context.Context) Query
	// PageSize sets how many rows one page returns.
	PageSize(n int) Query
	// PageState resumes from a previous page; it also limits Iter to one page.
	PageState(state []byte) Query
	// Idempotent marks the statement safe to retry or speculatively execute.
	Idempotent(idempotent bool) Query
	// Exec runs a statement that returns no rows.
	Exec() error
	// Iter runs the statement and returns its row iterator.
	Iter() Iter
}

// Iter iterates a query's rows.
//
// SDK seam: a thin projection of *gocql.Iter.
type Iter interface {
	// Scan reads the next row into dest; false at the end or on error.
	Scan(dest ...any) bool
	// PageState is the position after this page (empty when there are no more).
	PageState() []byte
	// Close ends the iteration and returns its error, coded.
	Close() error
}

// Batch is a group of statements applied together.
//
// SDK seam: a thin projection of *gocql.Batch.
type Batch interface {
	// Query appends a statement with bound values.
	Query(stmt string, values ...any)
	// WithContext binds ctx to the batch execution.
	WithContext(ctx context.Context) Batch
	// Size returns the number of statements.
	Size() int
}

// gocqlSession adapts *gocql.Session to Session.
type gocqlSession struct {
	// session is the dialed gocql session.
	session *gocql.Session
	// speculative is applied to idempotent queries (nil for none).
	speculative gocql.SpeculativeExecutionPolicy
}

// Query prepares stmt.
func (s *gocqlSession) Query(stmt string, values ...any) Query {
	return &gocqlQuery{
		query:       s.session.Query(stmt, values...),
		speculative: s.speculative,
	}
}

// Batch starts a batch of the given kind.
func (s *gocqlSession) Batch(kind BatchKind) Batch {
	typ := gocql.LoggedBatch
	if kind == UnloggedBatch {
		typ = gocql.UnloggedBatch
	}
	return &gocqlBatch{batch: s.session.NewBatch(typ)}
}

// ExecuteBatch applies a batch built by Batch; a foreign Batch is CodeInvalidInput.
func (s *gocqlSession) ExecuteBatch(b Batch) error {
	gb, ok := b.(*gocqlBatch)
	if !ok {
		return apperr.New(
			apperr.CodeInvalidInput,
			"cassandra: batch was not built by this session",
		)
	}
	return codeError(s.session.ExecuteBatch(gb.batch), "cassandra: execute batch")
}

// Close closes the session.
func (s *gocqlSession) Close() { s.session.Close() }

// gocqlQuery adapts *gocql.Query to Query.
type gocqlQuery struct {
	// query is the wrapped statement.
	query *gocql.Query
	// speculative is applied when the query is marked idempotent.
	speculative gocql.SpeculativeExecutionPolicy
}

// WithContext binds ctx.
func (q *gocqlQuery) WithContext(ctx context.Context) Query {
	q.query = q.query.WithContext(ctx)
	return q
}

// PageSize sets the page size.
func (q *gocqlQuery) PageSize(n int) Query {
	q.query = q.query.PageSize(n)
	return q
}

// PageState sets the resume position (and disables auto-paging).
func (q *gocqlQuery) PageState(state []byte) Query {
	q.query = q.query.PageState(state)
	return q
}

// Idempotent marks the statement idempotent and enables speculative execution.
func (q *gocqlQuery) Idempotent(idempotent bool) Query {
	q.query = q.query.Idempotent(idempotent)
	if idempotent && q.speculative != nil {
		q.query = q.query.SetSpeculativeExecutionPolicy(q.speculative)
	}
	return q
}

// Exec runs the statement.
func (q *gocqlQuery) Exec() error { return codeError(q.query.Exec(), "cassandra: exec") }

// Iter runs the statement and returns its iterator.
func (q *gocqlQuery) Iter() Iter { return &gocqlIter{iter: q.query.Iter()} }

// gocqlIter adapts *gocql.Iter to Iter.
type gocqlIter struct {
	// iter is the wrapped iterator.
	iter *gocql.Iter
}

// Scan reads the next row.
func (i *gocqlIter) Scan(dest ...any) bool { return i.iter.Scan(dest...) }

// PageState returns the position after this page.
func (i *gocqlIter) PageState() []byte { return i.iter.PageState() }

// Close ends the iteration.
func (i *gocqlIter) Close() error { return codeError(i.iter.Close(), "cassandra: read") }

// gocqlBatch adapts *gocql.Batch to Batch.
type gocqlBatch struct {
	// batch is the wrapped batch.
	batch *gocql.Batch
}

// Query appends a statement.
func (b *gocqlBatch) Query(stmt string, values ...any) { b.batch.Query(stmt, values...) }

// WithContext binds ctx.
func (b *gocqlBatch) WithContext(ctx context.Context) Batch {
	b.batch = b.batch.WithContext(ctx)
	return b
}

// Size returns the number of statements.
func (b *gocqlBatch) Size() int { return b.batch.Size() }

// codeError maps a driver error to a coded AppError at the client boundary: a
// timeout is CodeTimeout, an unavailable replica set or lost connection
// CodeUnavailable (both transient), a rejected statement CodeInvalidInput, and
// anything else CodeInternal.
func codeError(err error, msg string) error {
	if err == nil {
		return nil
	}
	var reqErr gocql.RequestError
	var netErr net.Error
	switch {
	case apperr.StdIs(err, context.DeadlineExceeded),
		apperr.StdIs(err, gocql.ErrTimeoutNoResponse):
		return apperr.Wrap(err, apperr.CodeTimeout, msg)
	case apperr.StdIs(err, context.Canceled):
		return apperr.Wrap(err, apperr.CodeCanceled, msg)
	case apperr.StdIs(err, gocql.ErrNoConnections),
		apperr.StdIs(err, gocql.ErrConnectionClosed),
		apperr.StdIs(err, gocql.ErrSessionClosed),
		apperr.As(err, &netErr):
		return apperr.Wrap(err, apperr.CodeUnavailable, msg)
	case apperr.As(err, &reqErr):
		return apperr.Wrap(err, requestErrorCode(reqErr.Code()), msg)
	default:
		return apperr.Wrap(err, apperr.CodeInternal, msg)
	}
}

// requestErrorCode maps a Cassandra protocol error code to an ErrorCode.
func requestErrorCode(code int) apperr.ErrorCode {
	switch code {
	case gocql.ErrCodeUnavailable, gocql.ErrCodeOverloaded, gocql.ErrCodeBootstrapping:
		return apperr.CodeUnavailable
	case gocql.ErrCodeWriteTimeout, gocql.ErrCodeReadTimeout:
		return apperr.CodeTimeout
	case gocql.ErrCodeSyntax,
		gocql.ErrCodeInvalid,
		gocql.ErrCodeUnprepared,
		gocql.ErrCodeConfig:
		return apperr.CodeInvalidInput
	case gocql.ErrCodeCredentials, gocql.ErrCodeUnauthorized:
		return apperr.CodeForbidden
	default:
		return apperr.CodeInternal
	}
}
