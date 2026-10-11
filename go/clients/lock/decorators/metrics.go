package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time assertion that *metricsDecorator satisfies the seam.
var _ interfaces.DistributedLock = (*metricsDecorator)(nil)

// metricsDecorator records per-operation counts (labelled by outcome) and
// latency via the core interfaces.Counter and interfaces.Histogram abstractions.
type metricsDecorator struct {
	// inner is the next lock in the decorator chain.
	inner interfaces.DistributedLock
	// ops counts operations by (lock, operation, outcome).
	ops interfaces.Counter
	// duration records operation latency in seconds by (lock, operation).
	duration interfaces.Histogram
	// name labels the lock instance.
	name string
}

// newMetricsDecorator registers the counter + histogram and wraps inner.
func newMetricsDecorator(
	inner interfaces.DistributedLock,
	name string,
	m interfaces.Metrics,
) *metricsDecorator {
	return &metricsDecorator{
		inner: inner,
		name:  name,
		ops: m.Counter(
			"lock_operations_total",
			"Total lock operations by outcome",
			"lock", "operation", "outcome",
		),
		duration: m.Histogram(
			"lock_operation_duration_seconds",
			"Lock operation duration in seconds",
			defaultBuckets(),
			"lock", "operation",
		),
	}
}

// Acquire records the acquire latency and outcome (acquired/contended/error).
func (d *metricsDecorator) Acquire(
	ctx context.Context,
	key string,
) (token string, acquired bool, err error) {
	start := time.Now()
	token, acquired, err = d.inner.Acquire(ctx, key)
	d.duration.Observe(time.Since(start).Seconds(), d.name, "acquire")
	d.ops.Inc(d.name, "acquire", acquireOutcome(acquired, err))
	return token, acquired, err
}

// Renew records the renew latency and outcome (held/lost/error).
func (d *metricsDecorator) Renew(ctx context.Context, key, token string) (bool, error) {
	start := time.Now()
	held, err := d.inner.Renew(ctx, key, token)
	d.duration.Observe(time.Since(start).Seconds(), d.name, "renew")
	d.ops.Inc(d.name, "renew", renewOutcome(held, err))
	return held, err
}

// Release records the release latency and outcome (ok/error).
func (d *metricsDecorator) Release(ctx context.Context, key, token string) error {
	start := time.Now()
	err := d.inner.Release(ctx, key, token)
	d.duration.Observe(time.Since(start).Seconds(), d.name, "release")
	d.ops.Inc(d.name, "release", okOrError(err))
	return err
}

// outcomeError is the outcome label every lock operation records when it fails.
const outcomeError = "error"

// acquireOutcome maps an Acquire result to a metric outcome label.
func acquireOutcome(acquired bool, err error) string {
	switch {
	case err != nil:
		return outcomeError
	case acquired:
		return "acquired"
	default:
		return "contended"
	}
}

// renewOutcome maps a Renew result to a metric outcome label.
func renewOutcome(held bool, err error) string {
	switch {
	case err != nil:
		return outcomeError
	case held:
		return "held"
	default:
		return "lost"
	}
}

// okOrError maps an error-only result to a metric outcome label.
func okOrError(err error) string {
	if err != nil {
		return outcomeError
	}
	return "ok"
}
