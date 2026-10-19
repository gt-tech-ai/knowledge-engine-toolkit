package interfaces

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// OutboxWriter is the producer side of a transactional outbox: a service enqueues the
// record announcing a change in the transaction that makes the change.
type OutboxWriter interface {
	// Enqueue writes rec as a pending row due now, on the transaction bound to ctx; with
	// none bound it fails INTERNAL, so a row never commits apart from its change. A
	// zero rec.ID gets a fresh one; a rec.ID already enqueued is left untouched.
	Enqueue(ctx context.Context, rec types.OutboxRecord) error
}

// OutboxStore is the durable side of a transactional outbox: the table the
// application writes rows into within its business transaction (OutboxWriter), and that
// the outbox relay leases, finalizes and summarizes.
type OutboxStore interface {
	OutboxWriter

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
	// DeleteSentBefore purges the delivered rows sent before cutoff (the retention
	// sweep) and returns how many it removed; pending and parked rows are kept.
	DeleteSentBefore(ctx context.Context, cutoff time.Time) (int, error)
}

// OutboxRepository is the OutboxStore run through the repository tier, the seam the
// outbox relay programs to. Build it with repos/outbox.New: Claim, which leases
// rows and counts an attempt, is never retried; the idempotent finalizers, Stats and
// the retention sweep get the tier's full decoration.
type OutboxRepository interface {
	OutboxStore
}

// OutboxSink delivers claimed outbox records to their destination (a queue, an
// object store, …).
type OutboxSink interface {
	// Send delivers recs and returns exactly one result per record, in order: nil
	// for a delivered record, the coded delivery error otherwise.
	Send(ctx context.Context, recs []types.OutboxRecord) []error
}
