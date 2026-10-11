package repository

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// repoMetricsSpec returns the spec naming the repository custom-op metrics —
// identical to the CRUD metric decorator's names/labels, so the get-or-create
// registry shares one set of collectors between the CRUD and custom-op paths (no
// double-registration).
func repoMetricsSpec() decorate.MetricsSpec {
	return decorate.MetricsSpec{
		OperationsName: "repository_operations_total",
		OperationsHelp: "Total repository operations",
		ErrorsName:     "repository_errors_total",
		ErrorsHelp:     "Total repository errors",
		DurationName:   "repository_operation_duration_seconds",
		DurationHelp:   "Repository operation duration",
		SubjectLabel:   "repo",
	}
}

// OpChain builds the custom-operation decoration chain for a repository named name,
// reproducing the order the CRUD decorator builder applies to custom ops
// (behavior-preserving) — outermost → innermost: tracing → metrics → logging → timeout
// → circuit-breaker → retry. Caching is a passthrough for custom ops, so it is omitted
// (behavior-identical). Any nil dependency (or a non-positive timeout) omits its
// middleware, exactly as the builder skips a decorator with a nil dep. Custom repo
// methods run through this via decorate.Exec[R].
func OpChain(
	name string,
	timeout time.Duration,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	retrier interfaces.Retrier,
	cb interfaces.CircuitBreaker,
) decorate.OpMiddleware {
	var mws []decorate.OpMiddleware
	if tracer != nil {
		mws = append(
			mws,
			decorate.NewTracing(tracer, name, "db.repository", "db.operation"),
		)
	}
	if metrics != nil {
		mws = append(mws, decorate.NewMetrics(metrics, name, repoMetricsSpec()))
	}
	if logger != nil {
		mws = append(mws, decorate.NewLogging(logger, name, "repo", "repository"))
	}
	if timeout > 0 {
		mws = append(mws, decorate.NewTimeout(timeout))
	}
	if cb != nil {
		mws = append(mws, decorate.NewCircuitBreaker(cb))
	}
	if retrier != nil {
		mws = append(mws, decorate.NewRetry(retrier))
	}
	return decorate.Chain(mws...)
}
