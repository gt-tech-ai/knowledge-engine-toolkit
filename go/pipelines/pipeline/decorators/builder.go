// Package decorators provides fluent decorator composition for pipelines.
// Decorators add cross-cutting concerns (logging, metrics) around any
// interfaces.Pipeline implementation without modifying it. The decorator
// implementations live once in go/foundation/decorator; this package keeps
// only the pipeline-tier fluent builder — its wrap order
// (ARCHITECTURE.md#decorator-order), its "pipeline" labels, and its coded-error
// recovery contract.
package decorators

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorator"
)

// tier is the pipeline label used for metric names/dimensions, log fields, and
// span attributes across every decorator this builder composes.
const tier = "pipeline"

// onPanic is the pipeline tier's recovery-error contract: a coded internal error
// (ARCHITECTURE.md#error-codes). The panic value is logged by the recovery decorator; the
// returned error is deliberately generic so it never leaks internals to callers.
func onPanic(_ string, _ any) error {
	return apperr.Internal("internal pipeline error: panic recovered")
}

// Builder constructs a decorated pipeline using the fluent API pattern.
// Each With* method enables a decorator; Build applies them inside-out:
// base → timeout → metrics → tracing → logging → recovery (recovery is outermost).
type Builder[In any, Out any] struct {
	// base is the underlying pipeline implementation to decorate.
	base interfaces.Pipeline[In, Out]

	// logger is the structured logger for the logging decorator. Nil means no logging.
	logger interfaces.Logger

	// metrics is the metrics provider for the metrics decorator. Nil means no metrics.
	metrics interfaces.Metrics

	// tracer is the distributed tracer for the tracing decorator. Nil = no tracing.
	tracer interfaces.Tracer

	// name is the pipeline name used for metric labels and log fields.
	name string

	// timeout is the per-execution deadline. Zero means no timeout.
	timeout time.Duration

	// recovery enables panic recovery as the outermost decorator.
	recovery bool
}

// NewBuilder creates a new decorator builder wrapping the given base pipeline.
// The name is used as a label/prefix for all cross-cutting concerns.
func NewBuilder[In, Out any](
	base interfaces.Pipeline[In, Out],
	name string,
) *Builder[In, Out] {
	return &Builder[In, Out]{base: base, name: name}
}

// WithLogging adds a logging decorator that logs execution entry and failures
// at Debug level (failures are suppressed in staging/prod where level=info; the
// error is logged once at the transport layer instead of at every decorator tier).
func (b *Builder[In, Out]) WithLogging(logger interfaces.Logger) *Builder[In, Out] {
	b.logger = logger
	return b
}

// WithMetrics adds a metrics decorator that records execution counts and
// durations via the interfaces.Metrics abstraction.
func (b *Builder[In, Out]) WithMetrics(m interfaces.Metrics) *Builder[In, Out] {
	b.metrics = m
	return b
}

// WithTracing adds a tracing decorator that creates a span per pipeline
// execution, so the request cascade is visible in Tempo.
func (b *Builder[In, Out]) WithTracing(tracer interfaces.Tracer) *Builder[In, Out] {
	b.tracer = tracer
	return b
}

// WithTimeout adds a per-execution deadline. Executions that exceed the
// timeout are cancelled via context and return a deadline-exceeded error.
func (b *Builder[In, Out]) WithTimeout(d time.Duration) *Builder[In, Out] {
	b.timeout = d
	return b
}

// WithRecovery adds a recovery decorator that catches panics and converts
// them to CodeInternal errors. Recovery is always outermost when enabled.
func (b *Builder[In, Out]) WithRecovery() *Builder[In, Out] {
	b.recovery = true
	return b
}

// Build constructs the decorated pipeline. Decorators are applied inside-out:
// base → timeout → metrics → tracing → logging → recovery (recovery is outermost).
// The composed decorator.Executor satisfies interfaces.Pipeline (identical method set).
func (b *Builder[In, Out]) Build() interfaces.Pipeline[In, Out] {
	var p decorator.Executor[In, Out] = b.base

	if b.timeout > 0 {
		p = decorator.Timeout(p, b.timeout)
	}
	if b.metrics != nil {
		p = decorator.Metrics(p, tier, b.name, b.metrics, decorator.DefaultBuckets())
	}
	if b.tracer != nil {
		p = decorator.Tracing(p, b.tracer, tier, b.name)
	}
	if b.logger != nil {
		p = decorator.Logging(p, b.logger, tier, b.name)
	}
	if b.recovery {
		p = decorator.Recovery(p, b.logger, tier, b.name, onPanic)
	}

	return p
}
