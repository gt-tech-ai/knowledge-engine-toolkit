// Package stub is the default analytics store: it accepts every write and answers
// every query with an empty result, so the graph boots with no Cassandra.
package stub

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Compile-time interface assertions.
var (
	_ interfaces.AnalyticsStore = (*Store)(nil)
	_ interfaces.RowStream      = (*emptyStream)(nil)
)

// Store discards writes and returns empty streams.
type Store struct{}

// New returns the stub store.
func New() *Store { return &Store{} }

// Start does nothing.
func (*Store) Start(context.Context) error { return nil }

// Stop does nothing.
func (*Store) Stop(context.Context) error { return nil }

// Aggregate returns a stream with one empty, final page.
func (*Store) Aggregate(
	context.Context,
	types.AggregateQuery,
) (interfaces.RowStream, error) {
	return &emptyStream{}, nil
}

// Write discards facts.
func (*Store) Write(context.Context, []types.Fact) error { return nil }

// emptyStream is a RowStream with no rows.
type emptyStream struct{}

// Next returns no rows and no more pages.
func (*emptyStream) Next(
	context.Context,
) ([]types.Row, bool, error) {
	return nil, false, nil
}

// ResumeToken is empty: there is nothing to resume.
func (*emptyStream) ResumeToken() []byte { return nil }

// Close does nothing.
func (*emptyStream) Close() error { return nil }
