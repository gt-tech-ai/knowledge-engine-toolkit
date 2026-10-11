package decorate

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultBuckets returns the histogram bucket boundaries (seconds) for operation
// latency — the same set the repo and service metric decorators use.
func defaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// MetricsSpec names the counters/histogram a metrics middleware registers, so the
// same middleware serves the repo tier (repository_* / "repo") and the service tier
// (service_* / "service"). The registry is get-or-create, so a spec matching the
// tier's CRUD metric decorator shares that decorator's collectors (no double-register).
type MetricsSpec struct {
	// OperationsName and OperationsHelp name and describe the operations counter.
	OperationsName, OperationsHelp string
	// ErrorsName and ErrorsHelp name and describe the errors counter.
	ErrorsName, ErrorsHelp string
	// DurationName and DurationHelp name and describe the duration histogram.
	DurationName, DurationHelp string
	// SubjectLabel is the first label key (e.g. "repo" or "service"); "operation" is
	// always the second.
	SubjectLabel string
}

// metricsMW records an operation count + latency, plus an error count on failure.
type metricsMW struct {
	// ops counts every operation (labelled by name and operation).
	ops interfaces.Counter
	// errs counts failed operations (labelled by name and operation).
	errs interfaces.Counter
	// dur observes operation latency in seconds (labelled by name and operation).
	dur interfaces.Histogram
	// name is the subject label value identifying the repo/service instance.
	name string
}

// NewMetrics returns a metrics middleware for name, registering (get-or-create) the
// counters/histogram named by spec, labelled (SubjectLabel, "operation").
func NewMetrics(m interfaces.Metrics, name string, spec MetricsSpec) OpMiddleware {
	return metricsMW{
		name: name,
		ops: m.Counter(
			spec.OperationsName,
			spec.OperationsHelp,
			spec.SubjectLabel,
			"operation",
		),
		errs: m.Counter(spec.ErrorsName, spec.ErrorsHelp, spec.SubjectLabel, "operation"),
		dur: m.Histogram(
			spec.DurationName,
			spec.DurationHelp,
			defaultBuckets(),
			spec.SubjectLabel,
			"operation",
		),
	}
}

// WrapOp records one operation count + latency for op, and an error count on failure.
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
