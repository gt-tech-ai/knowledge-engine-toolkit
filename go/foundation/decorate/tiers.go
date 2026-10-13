package decorate

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
)

// Observability carries the collaborators the observability decorators use at one
// tier. A nil one skips its decorator, so the zero value observes nothing.
type Observability struct {
	// Logger backs the logging decorator.
	Logger interfaces.Logger

	// Metrics backs the metrics decorator.
	Metrics interfaces.Metrics

	// Tracer backs the tracing decorator.
	Tracer interfaces.Tracer
}

// Tiers is the observability bundle for each tier of a request. The request workflow
// is the one boundary observed in every environment; a lower tier is observed only when
// its decoration switch is on, so a request is logged, metered and traced once unless a
// developer asks for more.
type Tiers struct {
	// Workflow observes the request workflow.
	Workflow Observability

	// Pipeline observes each pipeline stage.
	Pipeline Observability

	// Service observes each service operation.
	Service Observability

	// Repository observes each repository operation.
	Repository Observability
}

// NewTiers builds each tier's bundle from cfg over the service's logger, metrics and
// tracer: the workflow tier always gets all three, a lower tier only when its switch is
// on (and otherwise the zero Observability, whose decorators are skipped).
func NewTiers(
	cfg decoration.Config,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
) Tiers {
	full := Observability{Logger: logger, Metrics: metrics, Tracer: tracer}
	pick := func(on bool) Observability {
		if on {
			return full
		}
		return Observability{}
	}
	return Tiers{
		Workflow:   full,
		Pipeline:   pick(cfg.Pipelines),
		Service:    pick(cfg.Services),
		Repository: pick(cfg.Repositories),
	}
}
