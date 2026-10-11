package interfaces

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// OutboxStore is the durable side of a transactional outbox: the table the
// application writes rows into within its business transaction, and that the
// outbox relay leases, finalizes and summarizes.
type OutboxStore interface {
	// Claim leases up to limit pending rows of lane whose next attempt is due (on
	// the database clock) with FOR UPDATE SKIP LOCKED semantics, so concurrent
	// claimers receive disjoint rows. It counts the attempt (the returned Attempts
	// includes it) and is side-effecting, so a caller must not blindly retry it.
	Claim(ctx context.Context, lane string, limit int) ([]types.OutboxRecord, error)
	// MarkSent records that the row was delivered; it is never claimed again.
	MarkSent(ctx context.Context, id uuid.UUID) error
	// Retry makes the row claimable again at next, recording lastErr.
	Retry(ctx context.Context, id uuid.UUID, next time.Time, lastErr string) error
	// Park takes the row out of rotation after its final failed attempt, recording lastErr.
	Park(ctx context.Context, id uuid.UUID, lastErr string) error
	// Stats summarizes lane's pending and parked rows.
	Stats(ctx context.Context, lane string) (types.OutboxStats, error)
}

// OutboxSink delivers claimed outbox records to their destination (a queue, an
// object store, …).
type OutboxSink interface {
	// Send delivers recs and returns exactly one result per record, in order: nil
	// for a delivered record, the coded delivery error otherwise.
	Send(ctx context.Context, recs []types.OutboxRecord) []error
}
