package decorators

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	workflowscfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workflows"
)

// Decorate wraps wf in the standard workflow stack keyed by op: logging,
// metrics, tracing, the cfg.Timeout deadline and panic recovery, composed by the
// Builder in its documented order (recovery outermost). A nil logger, metrics or
// tracer leaves that layer out, and a zero cfg.Timeout applies no deadline.
func Decorate[In, Out any](
	wf interfaces.Workflow[In, Out],
	op string,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	cfg workflowscfg.Config,
) interfaces.Workflow[In, Out] {
	return NewBuilder(wf, op).
		WithLogging(logger).
		WithMetrics(metrics).
		WithTracing(tracer).
		WithTimeout(cfg.Timeout).
		WithRecovery().
		Build()
}
