package decorate

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Outbox runs each outbox-store operation through a tier: Enqueue and Claim on their own
// Subject (a repository's runs them without retry: an enqueue runs inside the caller's
// transaction, and a claim leases rows and counts an attempt), every other operation on
// the rest.
type Outbox struct {
	// inner is the wrapped store.
	inner interfaces.OutboxStore

	// claim runs Claim.
	claim Subject

	// op runs every other operation.
	op Subject
}

// NewOutbox returns inner with Claim run through claim and every other operation
// through rest.
// claim must not be nil.
// rest must not be nil.
func NewOutbox(claim, rest *Subject, inner interfaces.OutboxStore) Outbox {
	return Outbox{inner: inner, claim: *claim, op: *rest}
}

// Enqueue writes rec in the caller's transaction, run like Claim: once, since a retry
// inside the transaction would follow a failed statement.
// rec must not be nil.
func (r *Outbox) Enqueue(ctx context.Context, recRef *types.OutboxRecord) error {
	rCopy := *r

	rec := *recRef

	_, err := rCopy.claim.Exec(
		ctx,
		"enqueue",
		func(ctx context.Context) (struct{}, error) {
			return struct{}{}, rCopy.inner.Enqueue(ctx, &rec)
		},
	)

	return err
}

// Claim leases up to limit due rows of lane's partition; see
// interfaces.OutboxStore.
func (r *Outbox) Claim(
	ctx context.Context,
	lane string,
	partition, limit int,
) ([]types.OutboxRecord, error) {
	rCopy := *r

	return rCopy.claim.Exec(
		ctx,
		"claim",
		func(ctx context.Context) ([]types.OutboxRecord, error) {
			return rCopy.inner.Claim(ctx, lane, partition, limit)
		},
	)
}

// MarkSent records that row id was delivered.
func (r *Outbox) MarkSent(ctx context.Context, id uuid.UUID) error {
	rCopy := *r

	return rCopy.do(ctx, "mark_sent", func(ctx context.Context) error {
		return rCopy.inner.MarkSent(ctx, id)
	})
}

// Retry makes row id claimable again at next, recording lastErr.
func (r *Outbox) Retry(
	ctx context.Context,
	identifier uuid.UUID,
	next time.Time,
	lastErr string,
) error {
	rCopy := *r

	return rCopy.do(ctx, "retry", func(ctx context.Context) error {
		return rCopy.inner.Retry(ctx, identifier, next, lastErr)
	})
}

// Park takes row id out of rotation, recording lastErr.
func (r *Outbox) Park(ctx context.Context, id uuid.UUID, lastErr string) error {
	rCopy := *r

	return rCopy.do(ctx, "park", func(ctx context.Context) error {
		return rCopy.inner.Park(ctx, id, lastErr)
	})
}

// Stats summarizes lane's pending and parked rows.
func (r *Outbox) Stats(
	ctx context.Context,
	lane string,
) (types.OutboxStats, error) {
	rCopy := *r

	return rCopy.op.Exec(
		ctx,
		"stats",
		func(ctx context.Context) (types.OutboxStats, error) {
			return rCopy.inner.Stats(ctx, lane)
		},
	)
}

// DeleteSentBefore purges the rows delivered before cutoff and returns how many.
func (r *Outbox) DeleteSentBefore(
	ctx context.Context,
	cutoff time.Time,
) (int, error) {
	rCopy := *r

	return rCopy.op.Exec(
		ctx,
		"delete_sent_before",
		func(ctx context.Context) (int, error) {
			return rCopy.inner.DeleteSentBefore(ctx, cutoff)
		},
	)
}

// do runs an error-only operation through the chain.
func (r *Outbox) do(
	ctx context.Context,
	op string,
	fn func(context.Context) error,
) error {
	_, err := r.op.Exec(ctx, op, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})

	return err
}

var _ interfaces.OutboxStore = &Outbox{}
