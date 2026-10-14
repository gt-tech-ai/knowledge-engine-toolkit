package outbox

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
)

// decorated runs each outbox-store operation through the store tier.
type decorated struct {
	// inner is the backend store.
	inner interfaces.OutboxStore

	// op runs the operations through the tier.
	op decorate.Subject
}

// Decorate returns s with every operation run through the store tier as the store named
// name, decorated with deps.
func Decorate(
	s interfaces.OutboxStore,
	name string,
	deps decorate.Deps,
) interfaces.OutboxStore {
	return decorated{inner: s, op: store.Tier().Subject(name, deps)}
}

// Claim leases due rows of lane.
func (d decorated) Claim(
	ctx context.Context,
	lane string,
	limit int,
) ([]types.OutboxRecord, error) {
	return d.op.Exec(
		ctx,
		"Claim",
		func(ctx context.Context) ([]types.OutboxRecord, error) {
			return d.inner.Claim(ctx, lane, limit)
		},
	)
}

// MarkSent records that the row was delivered.
func (d decorated) MarkSent(ctx context.Context, id uuid.UUID) error {
	return d.do(ctx, "MarkSent", func(ctx context.Context) error {
		return d.inner.MarkSent(ctx, id)
	})
}

// Retry makes the row claimable again at next.
func (d decorated) Retry(
	ctx context.Context,
	id uuid.UUID,
	next time.Time,
	lastErr string,
) error {
	return d.do(ctx, "Retry", func(ctx context.Context) error {
		return d.inner.Retry(ctx, id, next, lastErr)
	})
}

// Park takes the row out of rotation.
func (d decorated) Park(ctx context.Context, id uuid.UUID, lastErr string) error {
	return d.do(ctx, "Park", func(ctx context.Context) error {
		return d.inner.Park(ctx, id, lastErr)
	})
}

// Stats summarizes lane.
func (d decorated) Stats(ctx context.Context, lane string) (types.OutboxStats, error) {
	return d.op.Exec(ctx, "Stats", func(ctx context.Context) (types.OutboxStats, error) {
		return d.inner.Stats(ctx, lane)
	})
}

// DeleteSentBefore purges the rows delivered before cutoff.
func (d decorated) DeleteSentBefore(ctx context.Context, cutoff time.Time) (int, error) {
	return d.op.Exec(ctx, "DeleteSentBefore", func(ctx context.Context) (int, error) {
		return d.inner.DeleteSentBefore(ctx, cutoff)
	})
}

// do runs the error-only operation op through the tier.
func (d decorated) do(
	ctx context.Context,
	op string,
	fn func(context.Context) error,
) error {
	_, err := d.op.Exec(ctx, op, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}
