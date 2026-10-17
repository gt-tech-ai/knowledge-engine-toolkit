package repository

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Outbox is the decorated outbox repository over store. Claim runs without retry: it
// leases rows and counts an attempt, so a second attempt would lease another batch. The
// finalizers (guarded on the row still pending), Stats and DeleteSentBefore are
// idempotent and run through the full chain.
func (b Base) Outbox(store interfaces.OutboxStore) interfaces.OutboxRepository {
	return outboxRepository{inner: store, claim: b.create, op: b.Subject}
}

// outboxRepository runs each outbox-store operation through the repository tier.
type outboxRepository struct {
	// inner is the decorated store.
	inner interfaces.OutboxStore

	// claim runs Claim: the decoration without retry.
	claim decorate.Subject

	// op runs every other operation.
	op decorate.Subject
}

// Claim leases up to limit due rows of lane; see interfaces.OutboxStore.
func (r outboxRepository) Claim(
	ctx context.Context,
	lane string,
	limit int,
) ([]types.OutboxRecord, error) {
	return r.claim.Exec(
		ctx,
		"Claim",
		func(ctx context.Context) ([]types.OutboxRecord, error) {
			return r.inner.Claim(ctx, lane, limit)
		},
	)
}

// MarkSent records that row id was delivered.
func (r outboxRepository) MarkSent(ctx context.Context, id uuid.UUID) error {
	return r.do(ctx, "MarkSent", func(ctx context.Context) error {
		return r.inner.MarkSent(ctx, id)
	})
}

// Retry makes row id claimable again at next, recording lastErr.
func (r outboxRepository) Retry(
	ctx context.Context,
	id uuid.UUID,
	next time.Time,
	lastErr string,
) error {
	return r.do(ctx, "Retry", func(ctx context.Context) error {
		return r.inner.Retry(ctx, id, next, lastErr)
	})
}

// Park takes row id out of rotation, recording lastErr.
func (r outboxRepository) Park(ctx context.Context, id uuid.UUID, lastErr string) error {
	return r.do(ctx, "Park", func(ctx context.Context) error {
		return r.inner.Park(ctx, id, lastErr)
	})
}

// Stats summarizes lane's pending and parked rows.
func (r outboxRepository) Stats(
	ctx context.Context,
	lane string,
) (types.OutboxStats, error) {
	return r.op.Exec(ctx, "Stats", func(ctx context.Context) (types.OutboxStats, error) {
		return r.inner.Stats(ctx, lane)
	})
}

// DeleteSentBefore purges the rows delivered before cutoff and returns how many.
func (r outboxRepository) DeleteSentBefore(
	ctx context.Context,
	cutoff time.Time,
) (int, error) {
	return r.op.Exec(ctx, "DeleteSentBefore", func(ctx context.Context) (int, error) {
		return r.inner.DeleteSentBefore(ctx, cutoff)
	})
}

// do runs an error-only operation through the chain.
func (r outboxRepository) do(
	ctx context.Context,
	op string,
	fn func(context.Context) error,
) error {
	_, err := r.op.Exec(ctx, op, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}
