// Package decorators wraps an AnalyticsStore with resilience and observability, in
// the order recovery → metrics → tracing → circuit breaker → timeout (outermost
// first), and wraps a clients/cassandra.Session's writes in the client stack. Apply
// them at the composition root; a nil collaborator or zero timeout skips its layer.
package decorators

import (
	"context"
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorator"
)

// tier labels the store's metrics, spans and logs.
const tier = "analytics"

// Builder composes the decorators around a base AnalyticsStore.
type Builder struct {
	// base is the store being decorated.
	base interfaces.AnalyticsStore
	// logger records recovered panics (nil = not logged).
	logger interfaces.Logger
	// metrics records executions, errors and durations (nil = off).
	metrics interfaces.Metrics
	// tracer opens a span per operation (nil = off).
	tracer interfaces.Tracer
	// cb sheds load while the store is failing (nil = off).
	cb interfaces.CircuitBreaker
	// name labels this store instance.
	name string
	// timeout bounds opening a stream, each write and each compaction (0 = off).
	timeout time.Duration
}

// NewBuilder starts a decorator chain around base, labelled name.
func NewBuilder(base interfaces.AnalyticsStore, name string) *Builder {
	return &Builder{base: base, name: name}
}

// WithLogger logs recovered panics.
func (b *Builder) WithLogger(l interfaces.Logger) *Builder { b.logger = l; return b }

// WithMetrics records analytics_executions_total, analytics_errors_total and
// analytics_execute_duration_seconds per operation.
func (b *Builder) WithMetrics(m interfaces.Metrics) *Builder { b.metrics = m; return b }

// WithTracer opens a span per operation.
func (b *Builder) WithTracer(t interfaces.Tracer) *Builder { b.tracer = t; return b }

// WithCircuitBreaker sheds operations while the breaker is open (CodeUnavailable).
func (b *Builder) WithCircuitBreaker(
	cb interfaces.CircuitBreaker,
) *Builder {
	b.cb = cb
	return b
}

// WithTimeout bounds opening a stream, each write and each compaction; an
// expired deadline is CodeTimeout.
func (b *Builder) WithTimeout(d time.Duration) *Builder { b.timeout = d; return b }

// Build returns the decorated store.
func (b *Builder) Build() interfaces.AnalyticsStore {
	return &store{
		base: b.base,
		aggregate: chain(
			b,
			"aggregate",
			execFunc[types.AggregateQuery, interfaces.RowStream](
				func(ctx context.Context, q types.AggregateQuery) (interfaces.RowStream, error) {
					return b.base.Aggregate(ctx, q)
				},
			),
		),
		write: chain(b, "write", execFunc[[]types.Fact, struct{}](
			func(ctx context.Context, facts []types.Fact) (struct{}, error) {
				return struct{}{}, b.base.Write(ctx, facts)
			})),
		compact: chain(b, "compact", execFunc[compactArgs, struct{}](
			func(ctx context.Context, a compactArgs) (struct{}, error) {
				c, ok := b.base.(interfaces.AnalyticsCompactor)
				if !ok {
					return struct{}{}, apperr.New(
						apperr.CodeInvalidInput,
						"analytics: store does not compact",
					)
				}
				return struct{}{}, c.Compact(ctx, a.cube, a.grain, a.org, a.bucket)
			})),
	}
}

// compactArgs are one Compact call's arguments.
type compactArgs struct {
	// bucket is the bucket start.
	bucket time.Time
	// cube, org name the partition.
	cube, org string
	// grain is the table's grain.
	grain types.Grain
}

// chain applies the layers to one operation, innermost first.
func chain[In, Out any](
	b *Builder,
	op string,
	e decorator.Executor[In, Out],
) decorator.Executor[In, Out] {
	name := b.name + "." + op
	if b.timeout > 0 {
		e = &codedTimeout[In, Out]{inner: decorator.Timeout(e, b.timeout)}
	}
	if b.cb != nil {
		e = decorator.CircuitBreaker(e, b.cb, func(err error) error {
			return apperr.Wrap(
				err,
				apperr.CodeUnavailable,
				"analytics: circuit breaker open",
			)
		})
	}
	if b.tracer != nil {
		e = decorator.Tracing(e, b.tracer, tier, name)
	}
	if b.metrics != nil {
		e = decorator.Metrics(e, tier, name, b.metrics, decorator.DefaultBuckets())
	}
	return decorator.Recovery(e, b.logger, tier, name, func(n string, r any) error {
		return apperr.New(
			apperr.CodeInternal,
			fmt.Sprintf("analytics %s panic recovered: %v", n, r),
		)
	})
}

// store is the decorated AnalyticsStore.
type store struct {
	// base provides the lifecycle.
	base interfaces.AnalyticsStore
	// aggregate is the decorated Aggregate.
	aggregate decorator.Executor[types.AggregateQuery, interfaces.RowStream]
	// write is the decorated Write.
	write decorator.Executor[[]types.Fact, struct{}]
	// compact is the decorated Compact.
	compact decorator.Executor[compactArgs, struct{}]
}

// Start starts the base store.
func (s *store) Start(ctx context.Context) error { return s.base.Start(ctx) }

// Stop stops the base store.
func (s *store) Stop(ctx context.Context) error { return s.base.Stop(ctx) }

// Aggregate runs the decorated Aggregate.
func (s *store) Aggregate(
	ctx context.Context,
	q types.AggregateQuery,
) (interfaces.RowStream, error) {
	return s.aggregate.Execute(ctx, q)
}

// Write runs the decorated Write.
func (s *store) Write(ctx context.Context, facts []types.Fact) error {
	_, err := s.write.Execute(ctx, facts)
	return err
}

// Compact runs the decorated Compact; a base without a compactor is
// CodeInvalidInput.
func (s *store) Compact(
	ctx context.Context,
	cube string,
	grain types.Grain,
	org string,
	bucket time.Time,
) error {
	_, err := s.compact.Execute(
		ctx,
		compactArgs{bucket: bucket, cube: cube, org: org, grain: grain},
	)
	return err
}

// execFunc adapts a function to decorator.Executor.
type execFunc[In, Out any] func(ctx context.Context, in In) (Out, error)

// Execute calls the function.
func (f execFunc[In, Out]) Execute(
	ctx context.Context,
	in In,
) (Out, error) {
	return f(ctx, in)
}

// codedTimeout turns an expired deadline into CodeTimeout.
type codedTimeout[In, Out any] struct {
	// inner is the deadline-bound executor.
	inner decorator.Executor[In, Out]
}

// Execute runs inner and codes a deadline overrun.
func (d *codedTimeout[In, Out]) Execute(ctx context.Context, in In) (Out, error) {
	out, err := d.inner.Execute(ctx, in)
	if err != nil && apperr.StdIs(err, context.DeadlineExceeded) &&
		apperr.Code(err) != apperr.CodeTimeout {
		return out, apperr.Wrap(
			err,
			apperr.CodeTimeout,
			"analytics: operation timed out",
		)
	}
	return out, err
}
