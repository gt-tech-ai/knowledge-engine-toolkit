package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// defaultBuckets returns the default histogram bucket boundaries for repository
// operation durations (in seconds).
func defaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// metricsDecorator records operation counts and durations using the core
// interfaces.Metrics abstraction.
type metricsDecorator[T any, P any, ID comparable] struct {
	// inner is the next repository in the decorator chain.
	inner interfaces.DecoratedRepository[T, P, ID]

	// operations counts total repository operations partitioned by repo and operation.
	operations interfaces.Counter

	// errors counts failed repository operations partitioned by repo and operation.
	errors interfaces.Counter

	// duration records operation latency in seconds partitioned by repo and operation.
	duration interfaces.Histogram

	// name is the repository name used as a label dimension.
	name string
}

// newMetricsDecorator creates a metricsDecorator with registered counters and histograms.
func newMetricsDecorator[T, P any, ID comparable](
	inner interfaces.DecoratedRepository[T, P, ID],
	name string,
	m interfaces.Metrics,
) *metricsDecorator[T, P, ID] {
	return &metricsDecorator[T, P, ID]{
		inner: inner,
		name:  name,
		operations: m.Counter(
			"repository_operations_total",
			"Total repository operations",
			"repo",
			"operation",
		),
		errors: m.Counter(
			"repository_errors_total",
			"Total repository errors",
			"repo",
			"operation",
		),
		duration: m.Histogram(
			"repository_operation_duration_seconds",
			"Repository operation duration",
			defaultBuckets(),
			"repo",
			"operation",
		),
	}
}

// observe records the operation count and its duration, plus an error count
// when the operation failed. Called once per operation with the start time.
func (d *metricsDecorator[T, P, ID]) observe(
	operation string,
	err error,
	start time.Time,
) {
	d.operations.Inc(d.name, operation)
	d.duration.Observe(time.Since(start).Seconds(), d.name, operation)
	if err != nil {
		d.errors.Inc(d.name, operation)
	}
}

// Get times the inner Get and records its metrics before returning.
func (d *metricsDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	start := time.Now()
	result, err := d.inner.Get(ctx, id)
	d.observe("Get", err, start)
	return result, err
}

// List times the inner List and records its metrics before returning.
func (d *metricsDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	start := time.Now()
	result, err := d.inner.List(ctx, params, page)
	d.observe("List", err, start)
	return result, err
}

// Create times the inner Create and records its metrics before returning.
func (d *metricsDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	start := time.Now()
	result, err := d.inner.Create(ctx, entity)
	d.observe("Create", err, start)
	return result, err
}

// Update times the inner Update and records its metrics before returning.
func (d *metricsDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	start := time.Now()
	result, err := d.inner.Update(ctx, id, entity)
	d.observe("Update", err, start)
	return result, err
}

// Delete times the inner Delete and records its metrics before returning.
func (d *metricsDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	start := time.Now()
	err := d.inner.Delete(ctx, id)
	d.observe("Delete", err, start)
	return err
}

// Exists times the inner Exists and records its metrics before returning.
func (d *metricsDecorator[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	start := time.Now()
	exists, err := d.inner.Exists(ctx, id)
	d.observe("Exists", err, start)
	return exists, err
}
