package analytics

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs Aggregate, Write and Compact of base through the client tier as the
// client named name. The lifecycle and health probes pass through undecorated: a probe
// is not an operation to retry, trace or count.
func Decorate(
	base interfaces.AnalyticsStore,
	name string,
	deps decorate.Deps,
) interfaces.AnalyticsStore {
	return &decorated{AnalyticsStore: base, Base: client.NewBase(name, deps)}
}

// decorated runs the store's data operations through the client tier.
type decorated struct {
	// AnalyticsStore is the wrapped store; it supplies the lifecycle and probes.
	interfaces.AnalyticsStore

	// Base runs the data operations.
	client.Base
}

var (
	_ interfaces.AnalyticsStore     = (*decorated)(nil)
	_ interfaces.AnalyticsCompactor = (*decorated)(nil)
)

// Aggregate runs the query as the operation "aggregate".
func (d *decorated) Aggregate(
	ctx context.Context,
	q types.AggregateQuery,
) (interfaces.RowStream, error) {
	return d.Exec(
		ctx,
		"aggregate",
		func(ctx context.Context) (interfaces.RowStream, error) {
			return d.AnalyticsStore.Aggregate(ctx, q)
		},
	)
}

// Write records facts as the operation "write".
func (d *decorated) Write(ctx context.Context, facts []types.Fact) error {
	return d.Do(ctx, "write", func(ctx context.Context) error {
		return d.AnalyticsStore.Write(ctx, facts)
	})
}

// Compact compacts one bucket as the operation "compact"; a base without a compactor
// is CodeInvalidInput.
func (d *decorated) Compact(
	ctx context.Context,
	cube string,
	grain types.Grain,
	org string,
	bucket time.Time,
) error {
	return d.Do(ctx, "compact", func(ctx context.Context) error {
		c, ok := d.AnalyticsStore.(interfaces.AnalyticsCompactor)
		if !ok {
			return apperr.New(
				apperr.CodeInvalidInput,
				"analytics: store does not compact",
			)
		}
		return c.Compact(ctx, cube, grain, org, bucket)
	})
}
