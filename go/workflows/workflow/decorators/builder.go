// Package decorators provides fluent decorator composition for workflows.
// Decorators add cross-cutting concerns (logging, metrics) around any
// interfaces.Workflow implementation without modifying it. The decorator
// implementations live once in go/foundation/decorator; this package keeps
// only the workflow-tier fluent builder — its wrap order
// (ARCHITECTURE.md#decorator-order), its "workflow" labels, and its recovery
// contract.
package decorators

import (
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorator"
)

// tier is the workflow label used for metric names/dimensions, log fields, and
// span attributes across every decorator this builder composes.
const tier = "workflow"

// onPanic is the workflow tier's recovery-error contract: a plain wrap that
// carries the panic value. The recovery decorator logs the value and stack; this
// returned error preserves the historical workflow-tier message.
func onPanic(_ string, r any) error {
	return apperr.New(apperr.CodeInternal, fmt.Sprintf("panic recovered: %v", r))
}

// Builder constructs a decorated workflow using the fluent API pattern. Each
// With* method enables a decorator; Build applies them inside-out: base →
// timeout → metrics → tracing → logging → recovery (recovery is outermost).
type Builder[In any, Out any] struct {
	// base is the underlying workflow implementation to decorate.
	base interfaces.Workflow[In, Out]

	// logger is the structured logger for the logging decorator. Nil means no
	// logging.
	logger interfaces.Logger

	// metrics is the metrics provider for the metrics decorator. Nil means no metrics.
	metrics interfaces.Metrics

	// tracer is the distributed tracer for the tracing decorator. Nil = no tracing.
	tracer interfaces.Tracer

	// name is the workflow name used for metric labels and log fields.
	name string

	// timeout is the per-execution timeout. Zero means no timeout decorator.
	timeout time.Duration

	// recovery enables the recovery decorator that catches panics.
	recovery bool
}

// NewBuilder creates a new decorator builder wrapping the given base workflow.
// The name is used as a label/prefix for all cross-cutting concerns.
func NewBuilder[In, Out any](
	base interfaces.Workflow[In, Out],
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

// WithTracing adds a tracing decorator that creates a span per workflow
// execution, so the request cascade is visible in Tempo.
func (b *Builder[In, Out]) WithTracing(tracer interfaces.Tracer) *Builder[In, Out] {
	b.tracer = tracer
	return b
}

// WithTimeout adds a timeout decorator that enforces a per-execution deadline.
func (b *Builder[In, Out]) WithTimeout(d time.Duration) *Builder[In, Out] {
	b.timeout = d
	return b
}

// WithRecovery adds a recovery decorator that catches panics and converts
// them to errors.
func (b *Builder[In, Out]) WithRecovery() *Builder[In, Out] {
	b.recovery = true
	return b
}

// Build constructs the decorated workflow. Decorators are applied inside-out:
// base → timeout → metrics → tracing → logging → recovery (recovery is outermost).
// The composed decorator.Executor satisfies interfaces.Workflow (identical method set).
func (b *Builder[In, Out]) Build() interfaces.Workflow[In, Out] {
	var w decorator.Executor[In, Out] = b.base

	if b.timeout > 0 {
		w = decorator.Timeout(w, b.timeout)
	}
	if b.metrics != nil {
		w = decorator.Metrics(w, tier, b.name, b.metrics, decorator.DefaultBuckets())
	}
	if b.tracer != nil {
		w = decorator.Tracing(w, b.tracer, tier, b.name)
	}
	if b.logger != nil {
		w = decorator.Logging(w, b.logger, tier, b.name)
	}
	if b.recovery {
		w = decorator.Recovery(w, b.logger, tier, b.name, onPanic)
	}

	return w
}
