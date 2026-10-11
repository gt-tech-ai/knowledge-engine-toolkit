package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// compile-time assertion that *metricsDecorator satisfies the seam.
var _ interfaces.ReplayBuffer = (*metricsDecorator)(nil)

// metricsDecorator records per-operation counts (labelled by outcome) and latency via the
// core interfaces.Counter and interfaces.Histogram abstractions.
type metricsDecorator struct {
	// inner is the next buffer in the decorator chain.
	inner interfaces.ReplayBuffer
	// ops counts operations by (buffer, operation, outcome).
	ops interfaces.Counter
	// duration records operation latency in seconds by (buffer, operation).
	duration interfaces.Histogram
	// name labels the buffer instance.
	name string
}

// newMetricsDecorator registers the counter + histogram and wraps inner.
func newMetricsDecorator(
	inner interfaces.ReplayBuffer,
	name string,
	m interfaces.Metrics,
) *metricsDecorator {
	return &metricsDecorator{
		inner: inner,
		name:  name,
		ops: m.Counter(
			"replaybuffer_operations_total",
			"Total replay-buffer operations by outcome",
			"buffer", "operation", "outcome",
		),
		duration: m.Histogram(
			"replaybuffer_operation_duration_seconds",
			"Replay-buffer operation duration in seconds",
			bufferDurationBuckets(),
			"buffer", "operation",
		),
	}
}

// outcome maps an error to the metric outcome label.
func outcome(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// Append records the append latency and outcome.
func (d *metricsDecorator) Append(
	ctx context.Context,
	key, msgID string,
	payload []byte,
) error {
	start := time.Now()
	err := d.inner.Append(ctx, key, msgID, payload)
	d.duration.Observe(time.Since(start).Seconds(), d.name, "append")
	d.ops.Inc(d.name, "append", outcome(err))
	return err
}

// ReplayAfter records the replay latency and outcome.
func (d *metricsDecorator) ReplayAfter(
	ctx context.Context,
	key, afterMsgID string,
) ([]interfaces.BufferedMessage, bool, error) {
	start := time.Now()
	msgs, complete, err := d.inner.ReplayAfter(ctx, key, afterMsgID)
	d.duration.Observe(time.Since(start).Seconds(), d.name, "replay_after")
	d.ops.Inc(d.name, "replay_after", outcome(err))
	return msgs, complete, err
}

// Prune records the prune latency and outcome.
func (d *metricsDecorator) Prune(ctx context.Context, key, upToMsgID string) error {
	start := time.Now()
	err := d.inner.Prune(ctx, key, upToMsgID)
	d.duration.Observe(time.Since(start).Seconds(), d.name, "prune")
	d.ops.Inc(d.name, "prune", outcome(err))
	return err
}

// Delete records the delete latency and outcome.
func (d *metricsDecorator) Delete(ctx context.Context, key string) error {
	start := time.Now()
	err := d.inner.Delete(ctx, key)
	d.duration.Observe(time.Since(start).Seconds(), d.name, "delete")
	d.ops.Inc(d.name, "delete", outcome(err))
	return err
}
