// Package decorator provides one generic implementation of the cross-cutting decorator
// stack (timeout, circuit breaker, metrics, tracing, logging, recovery) shared by every
// tier whose unit of work has the shape Execute(ctx, In) (Out, error) — pipelines and
// workflows today. Each tier keeps its own fluent builder (and therefore its own wrap
// order (ARCHITECTURE.md#decorator-order) and its tier-specific labels/recovery
// behavior); this package holds the decorator implementations those builders compose, so
// a decorator fix is made once instead of once per tier.
//
// The constructors take a caller-supplied inner Executor and return a decorated
// Executor — the builder chooses the order, this package does not hardcode one.
// Every returned decorator also implements Unwrapper so a test can reach the
// underlying handler through the stack.
package decorator

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Executor is the single-method shape the decorators wrap. interfaces.Pipeline
// and interfaces.Workflow have the identical method set, so one decorator set
// serves both tiers (and any future Execute(ctx, In) (Out, error) tier).
//
// interface-composition exemption — decoration mechanism primitive: the generic
// Execute(ctx, In) (Out, error) shape the decorators wrap, not an id-CRUD data-access
// surface, so it embeds no foundation generic.
type Executor[In any, Out any] interface {
	// Execute runs the wrapped unit of work for input, returning its result or an error.
	Execute(ctx context.Context, input In) (Out, error)
}

// Unwrapper exposes the inner Executor a decorator wraps, so a test can reach the
// underlying handler through the decorator stack (the Unwrap seam). Every decorator
// constructed here implements it.
//
// interface-composition exemption — decoration mechanism primitive: a single Unwrap() →
// Executor introspection port, not an id-CRUD data-access surface, so it embeds no
// foundation generic.
type Unwrapper[In any, Out any] interface {
	// Unwrap returns the inner Executor this decorator wraps.
	Unwrap() Executor[In, Out]
}

// DefaultBuckets returns the default histogram bucket boundaries (in seconds) for
// execution-duration metrics. Each call returns a fresh slice, so a caller may
// modify it without affecting other callers.
func DefaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// Timeout returns inner wrapped with a per-execution context deadline. A slow
// execution is cancelled via context when the deadline elapses.
func Timeout[In, Out any](inner Executor[In, Out], d time.Duration) Executor[In, Out] {
	return &timeout[In, Out]{inner: inner, dur: d}
}

// Metrics returns inner wrapped with execution-count, error-count, and
// duration-histogram metrics. tier prefixes the metric names
// (tier+"_executions_total" etc.) and is the label key (e.g. "pipeline"); name is
// the label value — the per-instance pipeline/workflow name — so each instance is
// its own time series (passing tier as the value would collapse every instance
// into one mislabeled series).
func Metrics[In, Out any](
	inner Executor[In, Out],
	tier, name string,
	m interfaces.Metrics,
	buckets []float64,
) Executor[In, Out] {
	return &metrics[In, Out]{
		inner: inner,
		name:  name,
		executions: m.Counter(
			tier+"_executions_total",
			"Total "+tier+" executions",
			tier,
		),
		errors: m.Counter(
			tier+"_errors_total",
			"Total "+tier+" execution errors",
			tier,
		),
		duration: m.Histogram(
			tier+"_execute_duration_seconds",
			cases(tier)+" execution duration",
			buckets,
			tier,
		),
	}
}

// Tracing returns inner wrapped with a trace span per execution, named
// name+".Execute" and carrying the tier+".name" attribute, so the request
// cascade is visible in Tempo.
func Tracing[In, Out any](
	inner Executor[In, Out],
	tracer interfaces.Tracer,
	tier, name string,
) Executor[In, Out] {
	return &tracing[In, Out]{inner: inner, tracer: tracer, tier: tier, name: name}
}

// Logging returns inner wrapped with entry/failure logging at Debug level (the
// error is logged once at the transport layer in staging/prod). tier is both the
// log-message prefix (tier+".Execute") and the field key; name is the field value.
func Logging[In, Out any](
	inner Executor[In, Out],
	logger interfaces.Logger,
	tier, name string,
) Executor[In, Out] {
	return &logging[In, Out]{inner: inner, logger: logger, tier: tier, name: name}
}

// Recovery returns inner wrapped with panic recovery (always outermost in the tier
// builders' order, ARCHITECTURE.md#decorator-order). A recovered panic is logged as
// tier+" panic recovered" and the returned error is produced by onPanic, so each tier
// keeps its own recovery-error contract (a coded error, a plain wrap, …). A nil onPanic
// falls back to a generic wrapped error, so the recovery path never itself panics on a
// missing contract.
func Recovery[In, Out any](
	inner Executor[In, Out],
	logger interfaces.Logger,
	tier, name string,
	onPanic func(name string, r any) error,
) Executor[In, Out] {
	return &recovery[In, Out]{
		inner:   inner,
		logger:  logger,
		tier:    tier,
		name:    name,
		onPanic: onPanic,
	}
}

// cases upper-cases the first byte of a tier label for metric help text (which
// reads "Pipeline execution duration"); tiers are ASCII words here.
func cases(tier string) string {
	if tier == "" {
		return tier
	}
	b := []byte(tier)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}

// timeout enforces a per-execution deadline by wrapping the context.
type timeout[In, Out any] struct {
	// inner is the wrapped executor the deadline is applied around.
	inner Executor[In, Out]
	// dur is the per-execution timeout imposed on the context.
	dur time.Duration
}

// Execute imposes the per-execution deadline on the context, then delegates to the
// inner executor (which is cancelled if the deadline elapses).
func (d *timeout[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	ctx, cancel := context.WithTimeout(ctx, d.dur)
	defer cancel()
	return d.inner.Execute(ctx, input)
}

// Unwrap returns the inner executor this decorator wraps.
func (d *timeout[In, Out]) Unwrap() Executor[In, Out] { return d.inner }

// metrics records execution counts and durations via interfaces.Metrics.
type metrics[In, Out any] struct {
	// inner is the wrapped executor whose executions are measured.
	inner Executor[In, Out]
	// executions counts every execution (labelled by name).
	executions interfaces.Counter
	// errors counts failed executions (labelled by name).
	errors interfaces.Counter
	// duration observes execution latency in seconds (labelled by name).
	duration interfaces.Histogram
	// name is the per-instance label value applied to every metric.
	name string
}

// Execute delegates to the inner executor, recording execution count and duration
// always and error count on failure.
func (d *metrics[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	start := time.Now()
	result, err := d.inner.Execute(ctx, input)
	d.executions.Inc(d.name)
	d.duration.Observe(time.Since(start).Seconds(), d.name)
	if err != nil {
		d.errors.Inc(d.name)
	}
	return result, err
}

// Unwrap returns the inner executor this decorator wraps.
func (d *metrics[In, Out]) Unwrap() Executor[In, Out] { return d.inner }

// tracing adds a distributed trace span per execution.
type tracing[In, Out any] struct {
	// inner is the wrapped executor the span is opened around.
	inner Executor[In, Out]
	// tracer opens the per-execution span.
	tracer interfaces.Tracer
	// tier is the attribute-key prefix (e.g. "pipeline"), used as tier+".name".
	tier string
	// name is the executor's name — the span name prefix and attribute value.
	name string
}

// Execute opens a span named name+".Execute" carrying the tier+".name" attribute,
// delegates to the inner executor, and records any error on the span.
func (d *tracing[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	ctx, span := d.tracer.Start(ctx, d.name+".Execute")
	defer span.End()
	span.SetAttribute(d.tier+".name", d.name)
	result, err := d.inner.Execute(ctx, input)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	}
	return result, err
}

// Unwrap returns the inner executor this decorator wraps.
func (d *tracing[In, Out]) Unwrap() Executor[In, Out] { return d.inner }

// logging logs execution entry and failure at Debug level.
type logging[In, Out any] struct {
	// inner is the wrapped executor whose entry/failure is logged.
	inner Executor[In, Out]
	// logger emits the Debug-level entry and failure logs.
	logger interfaces.Logger
	// tier is both the log-message prefix (tier+".Execute") and the field key.
	tier string
	// name is the field value identifying the executor instance.
	name string
}

// Execute logs entry at Debug, delegates to the inner executor, and logs a failure
// (with duration and error) at Debug.
func (d *logging[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	d.logger.WithContext(ctx).Debug(d.tier+".Execute", d.tier, d.name)
	start := time.Now()
	result, err := d.inner.Execute(ctx, input)
	duration := time.Since(start)

	if err != nil {
		d.logger.WithContext(ctx).Debug(
			d.tier+".Execute failed",
			d.tier, d.name,
			"duration", duration.String(),
			"error", err,
		)
	}
	return result, err
}

// Unwrap returns the inner executor this decorator wraps.
func (d *logging[In, Out]) Unwrap() Executor[In, Out] { return d.inner }

// recovery catches panics in the inner chain and converts them to an error via
// the tier-supplied onPanic.
type recovery[In, Out any] struct {
	// inner is the wrapped executor whose panics are recovered.
	inner Executor[In, Out]
	// logger records the recovered panic (with stack) at Error level.
	logger interfaces.Logger
	// onPanic converts a recovered panic into the tier's error contract; a nil
	// onPanic falls back to a generic wrapped error.
	onPanic func(name string, r any) error
	// tier is the log-message prefix (tier+" panic recovered") and field key.
	tier string
	// name is the field value identifying the executor instance.
	name string
}

// Execute delegates to the inner executor, recovering any panic into an error via
// onPanic (or a generic wrap) and logging it with its stack at Error level.
func (d *recovery[In, Out]) Execute(
	ctx context.Context,
	input In,
) (result Out, err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			if d.logger != nil {
				d.logger.WithContext(ctx).Error(
					d.tier+" panic recovered",
					d.tier, d.name,
					"panic", fmt.Sprint(r),
					"stack", string(stack),
				)
			}
			if d.onPanic != nil {
				err = d.onPanic(d.name, r)
			} else {
				err = apperr.New(
					apperr.CodeInternal,
					fmt.Sprintf("%s panic recovered: %v", d.tier, r),
				)
			}
		}
	}()
	return d.inner.Execute(ctx, input)
}

// Unwrap returns the inner executor this decorator wraps.
func (d *recovery[In, Out]) Unwrap() Executor[In, Out] { return d.inner }

// CircuitBreaker returns inner run through cb. When cb rejects the call without
// running inner (an open breaker), Execute returns onOpen(err) so the tier can
// code the rejection; an error from inner itself passes through unchanged.
func CircuitBreaker[In, Out any](
	inner Executor[In, Out],
	cb interfaces.CircuitBreaker,
	onOpen func(error) error,
) Executor[In, Out] {
	return &breaker[In, Out]{inner: inner, cb: cb, onOpen: onOpen}
}

// breaker runs each execution through a circuit breaker.
type breaker[In, Out any] struct {
	// inner is the protected executor.
	inner Executor[In, Out]
	// cb decides whether inner runs.
	cb interfaces.CircuitBreaker
	// onOpen converts a rejection into the tier's error.
	onOpen func(error) error
}

// Execute runs inner through the breaker, converting only a rejection with onOpen.
func (d *breaker[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	var out Out
	ran := false
	err := d.cb.Execute(func() error {
		ran = true
		var inner error
		out, inner = d.inner.Execute(ctx, input)
		return inner
	})
	if err != nil && !ran {
		return out, d.onOpen(err)
	}
	return out, err
}

// Unwrap returns the inner executor this decorator wraps.
func (d *breaker[In, Out]) Unwrap() Executor[In, Out] { return d.inner }
