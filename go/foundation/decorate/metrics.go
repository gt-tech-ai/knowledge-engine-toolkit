package decorate

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultBuckets returns the histogram bucket boundaries (seconds) for operation
// latency.
func defaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// metricsMW records the RED series of a tier: <tier>_operations_total,
// <tier>_errors_total and <tier>_operation_duration_seconds, labelled by the subject
// ("<tier>") and the operation.
type metricsMW struct {
	// ops counts every operation.
	ops interfaces.Counter

	// errs counts failed operations.
	errs interfaces.Counter

	// dur records each operation's duration in seconds.
	dur interfaces.Histogram

	// name is the decorated subject.
	name string
}

// NewMetrics returns the metrics middleware for the subject name in tier, creating
// the tier's instruments on m.
func NewMetrics(m interfaces.Metrics, tier, name string) OpMiddleware {
	return metricsMW{
		name: name,
		ops: m.Counter(
			tier+"_operations_total", "Total "+tier+" operations", tier, "operation",
		),
		errs: m.Counter(
			tier+"_errors_total", "Total "+tier+" operation errors", tier, "operation",
		),
		dur: m.Histogram(
			tier+"_operation_duration_seconds", tier+" operation duration",
			defaultBuckets(), tier, "operation",
		),
	}
}

// WrapOp runs next and records its count, duration and failure.
func (m metricsMW) WrapOp(
	ctx context.Context,
	op string,
	next func(context.Context) error,
) error {
	start := time.Now()
	err := next(ctx)
	m.ops.Inc(m.name, op)
	m.dur.Observe(time.Since(start).Seconds(), m.name, op)
	if err != nil {
		m.errs.Inc(m.name, op)
	}
	return err
}
