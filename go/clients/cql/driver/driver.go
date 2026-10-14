// Package driver adapts the gocql driver to the CQLSession contract: Dial opens a
// session on a cluster configuration (built by a cql kind's subpackage), and every
// driver error is coded at this boundary. Both cql kinds share it.
package driver

import (
	"context"
	"net"

	"github.com/gocql/gocql"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Dial creates a session on cluster; speculative is the execution policy its
// idempotent queries use (nil for none). A failed dial is CodeUnavailable.
func Dial(
	cluster *gocql.ClusterConfig,
	speculative gocql.SpeculativeExecutionPolicy,
) (interfaces.CQLSession, error) {
	s, err := cluster.CreateSession()
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeUnavailable, "cql: create session")
	}
	return &gocqlSession{session: s, speculative: speculative}, nil
}

// gocqlSession adapts *gocql.Session to interfaces.CQLSession.
type gocqlSession struct {
	// session is the dialed gocql session.
	session *gocql.Session
	// speculative is applied to idempotent queries (nil for none).
	speculative gocql.SpeculativeExecutionPolicy
}

// Query prepares stmt.
func (s *gocqlSession) Query(stmt string, values ...any) interfaces.CQLQuery {
	return &gocqlQuery{
		query:       s.session.Query(stmt, values...),
		speculative: s.speculative,
	}
}

// Batch starts a batch of the given kind.
func (s *gocqlSession) Batch(kind interfaces.CQLBatchKind) interfaces.CQLBatch {
	typ := gocql.LoggedBatch
	if kind == interfaces.CQLUnloggedBatch {
		typ = gocql.UnloggedBatch
	}
	return &gocqlBatch{batch: s.session.NewBatch(typ)}
}

// ExecuteBatch applies a batch built by Batch; a foreign Batch is CodeInvalidInput.
func (s *gocqlSession) ExecuteBatch(b interfaces.CQLBatch) error {
	gb, ok := b.(*gocqlBatch)
	if !ok {
		return apperr.New(
			apperr.CodeInvalidInput,
			"cql: batch was not built by this session",
		)
	}
	return codeError(s.session.ExecuteBatch(gb.batch), "cql: execute batch")
}

// Close closes the session.
func (s *gocqlSession) Close() { s.session.Close() }

// gocqlQuery adapts *gocql.Query to interfaces.CQLQuery.
type gocqlQuery struct {
	// query is the wrapped statement.
	query *gocql.Query
	// speculative is applied when the query is marked idempotent.
	speculative gocql.SpeculativeExecutionPolicy
}

// WithContext binds ctx.
func (q *gocqlQuery) WithContext(ctx context.Context) interfaces.CQLQuery {
	q.query = q.query.WithContext(ctx)
	return q
}

// PageSize sets the page size.
func (q *gocqlQuery) PageSize(n int) interfaces.CQLQuery {
	q.query = q.query.PageSize(n)
	return q
}

// PageState sets the resume position (and disables auto-paging).
func (q *gocqlQuery) PageState(state []byte) interfaces.CQLQuery {
	q.query = q.query.PageState(state)
	return q
}

// Idempotent marks the statement idempotent and enables speculative execution.
func (q *gocqlQuery) Idempotent(idempotent bool) interfaces.CQLQuery {
	q.query = q.query.Idempotent(idempotent)
	if idempotent && q.speculative != nil {
		q.query = q.query.SetSpeculativeExecutionPolicy(q.speculative)
	}
	return q
}

// Exec runs the statement.
func (q *gocqlQuery) Exec() error { return codeError(q.query.Exec(), "cql: exec") }

// Iter runs the statement and returns its iterator.
func (q *gocqlQuery) Iter() interfaces.CQLIter { return &gocqlIter{iter: q.query.Iter()} }

// gocqlIter adapts *gocql.Iter to interfaces.CQLIter.
type gocqlIter struct {
	// iter is the wrapped iterator.
	iter *gocql.Iter
}

// Scan reads the next row.
func (i *gocqlIter) Scan(dest ...any) bool { return i.iter.Scan(dest...) }

// PageState returns the position after this page.
func (i *gocqlIter) PageState() []byte { return i.iter.PageState() }

// Close ends the iteration.
func (i *gocqlIter) Close() error { return codeError(i.iter.Close(), "cql: read") }

// gocqlBatch adapts *gocql.Batch to interfaces.CQLBatch.
type gocqlBatch struct {
	// batch is the wrapped batch.
	batch *gocql.Batch
}

// Query appends a statement.
func (b *gocqlBatch) Query(stmt string, values ...any) { b.batch.Query(stmt, values...) }

// WithContext binds ctx.
func (b *gocqlBatch) WithContext(ctx context.Context) interfaces.CQLBatch {
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
