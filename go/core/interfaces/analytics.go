package interfaces

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// AnalyticsStore persists analytics facts and answers aggregate queries over them.
// Writes are idempotent per Fact.IdempotencyKey (a redelivered fact is a no-op), so
// a consumer may retry them freely. Aggregate returns a pull-based RowStream: rows
// are fetched a page at a time as the caller asks, so a slow reader slows the store
// instead of growing memory.
type AnalyticsStore interface {
	// Lifecycle starts and stops the store's connections.
	Lifecycle
	// Aggregate opens a stream of the partial rows answering q. q.OrgID scopes the
	// read and q.ResumeToken, when set, resumes a previous stream after its last
	// delivered page.
	Aggregate(ctx context.Context, q types.AggregateQuery) (RowStream, error)
	// Write upserts facts; writing the same fact twice leaves the store unchanged.
	Write(ctx context.Context, facts []types.Fact) error
}

// RowStream is a pull-based, page-at-a-time result of AnalyticsStore.Aggregate.
type RowStream interface {
	// Next returns the next page of rows and whether more pages may follow; a page
	// may be empty (every row filtered out) while more is still true.
	Next(ctx context.Context) (rows []types.Row, more bool, err error)
	// ResumeToken is an opaque position after the last page Next returned; pass it
	// as AggregateQuery.ResumeToken to continue the stream elsewhere.
	ResumeToken() []byte
	// Close releases the stream's resources; it is safe to call more than once.
	Close() error
}

// AnalyticsCompactor is optionally implemented by an AnalyticsStore that keeps one
// row per fact: Compact merges a closed bucket's per-fact rows into one row per
// group, preserving every answer. It is idempotent and safe to rerun.
type AnalyticsCompactor interface {
	// Compact merges the per-fact rows of org's cube at grain in the bucket that
	// starts at bucket.
	Compact(
		ctx context.Context,
		cube string,
		grain types.Grain,
		org string,
		bucket time.Time,
	) error
}
