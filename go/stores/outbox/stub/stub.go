// Package stub is the stub kind of the outbox store (stores/outbox): an outbox with no
// rows, so a relay graph boots and runs with no database. Enqueue discards its record,
// Claim finds nothing and every finalize is a no-op.
package stub

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Store is the empty outbox store.
type Store struct{}

// Compile-time check: a Store is an OutboxStore.
var _ interfaces.OutboxStore = Store{}

// New returns the empty outbox store.
func New() Store { return Store{} }

// Enqueue discards rec.
func (Store) Enqueue(context.Context, types.OutboxRecord) error { return nil }

// Claim finds no rows.
func (Store) Claim(context.Context, string, int) ([]types.OutboxRecord, error) {
	return nil, nil
}

// MarkSent does nothing.
func (Store) MarkSent(context.Context, uuid.UUID) error { return nil }

// Retry does nothing.
func (Store) Retry(context.Context, uuid.UUID, time.Time, string) error { return nil }

// Park does nothing.
func (Store) Park(context.Context, uuid.UUID, string) error { return nil }

// Stats reports an empty lane.
func (Store) Stats(context.Context, string) (types.OutboxStats, error) {
	return types.OutboxStats{}, nil
}

// DeleteSentBefore removes nothing.
func (Store) DeleteSentBefore(context.Context, time.Time) (int, error) { return 0, nil }
