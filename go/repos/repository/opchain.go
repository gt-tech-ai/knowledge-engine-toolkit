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

// opChain builds the custom-operation decoration chain for a repository named name,
// reproducing the order the CRUD decorator builder applies to custom ops
// (behavior-preserving) — outermost → innermost: tracing → metrics → logging → timeout
// → circuit-breaker → retry. Caching is a passthrough for custom ops, so it is omitted
// (behavior-identical). Any nil dependency (or a non-positive timeout) omits its
// middleware, exactly as the builder skips a decorator with a nil dep. Custom repo
// methods run through this via decorate.Exec[R].
func opChain(
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

// OpDeps bundles the collaborators a custom-operation chain wraps, so a repository
// constructor takes one value instead of six positional ones and the composition root
// builds it once per store. A nil collaborator, or a non-positive Timeout, omits its
// middleware.
type OpDeps struct {
	// Logger, when non-nil, adds the logging middleware.
	Logger interfaces.Logger

	// Metrics, when non-nil, adds the metrics middleware.
	Metrics interfaces.Metrics

	// Tracer, when non-nil, adds the tracing middleware.
	Tracer interfaces.Tracer

	// Retrier, when non-nil, adds the retry middleware.
	Retrier interfaces.Retrier

	// Breaker, when non-nil, adds the circuit-breaker middleware.
	Breaker interfaces.CircuitBreaker

	// Timeout bounds one operation; zero or negative skips the timeout middleware.
	Timeout time.Duration
}
