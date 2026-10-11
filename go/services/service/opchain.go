package service

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// svcMetricsSpec returns the spec naming the service custom-op metrics — identical
// to the CRUD metric decorator's names/labels, so the get-or-create registry
// shares one set of collectors between the CRUD and custom-op paths.
func svcMetricsSpec() decorate.MetricsSpec {
	return decorate.MetricsSpec{
		OperationsName: "service_operations_total",
		OperationsHelp: "Total service operations",
		ErrorsName:     "service_errors_total",
		ErrorsHelp:     "Total service errors",
		DurationName:   "service_operation_duration_seconds",
		DurationHelp:   "Service operation duration",
		SubjectLabel:   "service",
	}
}

// OpChain builds the custom-operation decoration chain for a service named name,
// reproducing the order the CRUD decorator builder applies to custom ops
// (behavior-preserving) — outermost → innermost: recovery → tracing → metrics → logging
// → auth → timeout. Recovery is ALWAYS outermost (the builder always adds it; a nil
// logger still recovers, just without logging). Validation is entity-based and a
// passthrough for custom ops, so it is omitted. Any nil dependency (nil authFn, or a
// non-positive timeout) omits its middleware, matching the builder. Custom service
// methods run through this via decorate.Exec[R].
func OpChain(
	name string,
	timeout time.Duration,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	authFn func(ctx context.Context, action string) error,
) decorate.OpMiddleware {
	mws := []decorate.OpMiddleware{decorate.NewRecovery(logger, name)}
	if tracer != nil {
		mws = append(
			mws,
			decorate.NewTracing(tracer, name, "service.name", "service.operation"),
		)
	}
	if metrics != nil {
		mws = append(mws, decorate.NewMetrics(metrics, name, svcMetricsSpec()))
	}
	if logger != nil {
		mws = append(mws, decorate.NewLogging(logger, name, "service", "service"))
	}
	if authFn != nil {
		mws = append(mws, decorate.NewAuth(name, authFn))
	}
	if timeout > 0 {
		mws = append(mws, decorate.NewTimeout(timeout))
	}
	return decorate.Chain(mws...)
}
