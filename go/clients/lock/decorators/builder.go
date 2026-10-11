// Package decorators composes cross-cutting concerns — resilience (retry,
// circuit-breaker, timeout) and observability (logging, metrics, tracing) —
// around any core/interfaces.DistributedLock, without touching the backend.
//
// The stack mirrors the repository/service decorator builders and their ordering
// (ARCHITECTURE.md#decorator-order): resilience nests closest to the backend and the
// observability trio wraps it as Tracing → Metrics → Logging (Tracing outermost, Logging
// innermost), so a span/metric covers the whole resilient operation including retries.
// Every With* is nil-safe: a nil collaborator skips that decorator, so dev/test paths run
// undecorated.
package decorators

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// defaultBuckets returns the histogram bucket boundaries (seconds) for lock
// operation durations. Matches Prometheus DefBuckets.
func defaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// Builder composes decorators around a base DistributedLock using a fluent API.
type Builder struct {
	// base is the underlying lock implementation to decorate.
	base interfaces.DistributedLock

	// logger drives the logging decorator. Nil means no logging decorator.
	logger interfaces.Logger

	// metrics drives the metrics decorator. Nil means no metrics decorator.
	metrics interfaces.Metrics

	// tracer drives the tracing decorator. Nil means no tracing decorator.
	tracer interfaces.Tracer

	// retrier drives the retry decorator (transient-error retries). Nil = no retry.
	retrier interfaces.Retrier

	// cb drives the circuit-breaker decorator. Nil means no circuit breaker.
	cb interfaces.CircuitBreaker

	// name labels the lock instance in metrics, spans, and logs.
	name string

	// timeout is the per-operation deadline. Zero means no timeout decorator.
	timeout time.Duration
}

// NewBuilder creates a decorator builder wrapping base. name labels the lock in
// metrics, spans, and logs.
func NewBuilder(base interfaces.DistributedLock, name string) *Builder {
	return &Builder{base: base, name: name}
}

// WithLogging adds a logging decorator: each operation logs at Debug with its
// outcome, and failures at Error.
func (b *Builder) WithLogging(logger interfaces.Logger) *Builder {
	b.logger = logger
	return b
}

// WithMetrics adds a metrics decorator recording per-operation counts (by
// outcome) and latency via the interfaces.Metrics abstraction.
func (b *Builder) WithMetrics(m interfaces.Metrics) *Builder {
	b.metrics = m
	return b
}

// WithTracing adds a tracing decorator that opens a client span per operation so
// the lock's Redis round-trips are visible in Tempo.
func (b *Builder) WithTracing(tracer interfaces.Tracer) *Builder {
	b.tracer = tracer
	return b
}

// WithRetry adds a retry decorator that retries an operation on a transient
// backend error. Contention (acquired=false with a nil error) is NOT retried —
// only a non-nil error triggers a retry.
func (b *Builder) WithRetry(r interfaces.Retrier) *Builder {
	b.retrier = r
	return b
}

// WithCircuitBreaker adds a circuit breaker that trips on repeated backend
// failures. Unlike the cache breaker it does NOT fabricate a result: when open
// it surfaces the error (fail-closed), because fabricating acquired=true would
// admit a duplicate operation and acquired=false would wedge every pod — the caller
// must decide how to degrade.
func (b *Builder) WithCircuitBreaker(cb interfaces.CircuitBreaker) *Builder {
	b.cb = cb
	return b
}

// WithTimeout adds a per-operation deadline; operations exceeding it are
// cancelled via context.
func (b *Builder) WithTimeout(d time.Duration) *Builder {
	b.timeout = d
	return b
}

// Build constructs the decorated lock. Decorators are applied inside-out:
//
//	base → retry → circuitBreaker → timeout → logging → metrics → tracing
//
// so resilience nests closest to the backend and the observability trio wraps it as
// Tracing → Metrics → Logging (Tracing outermost, Logging innermost) per
// ARCHITECTURE.md#decorator-order. A nil collaborator skips its decorator, so an all-nil
// builder returns the base unchanged.
func (b *Builder) Build() interfaces.DistributedLock {
	lk := b.base

	if b.retrier != nil {
		lk = &retryDecorator{inner: lk, retrier: b.retrier}
	}
	if b.cb != nil {
		lk = &circuitBreakerDecorator{inner: lk, cb: b.cb}
	}
	if b.timeout > 0 {
		lk = &timeoutDecorator{inner: lk, timeout: b.timeout}
	}
	// Observability trio, innermost → outermost: Logging → Metrics → Tracing, so
	// the composed nesting is Tracing → Metrics → Logging (Logging innermost).
	if b.logger != nil {
		lk = &loggingDecorator{inner: lk, logger: b.logger, name: b.name}
	}
	if b.metrics != nil {
		lk = newMetricsDecorator(lk, b.name, b.metrics)
	}
	if b.tracer != nil {
		lk = &tracingDecorator{inner: lk, tracer: b.tracer, name: b.name}
	}

	return lk
}
