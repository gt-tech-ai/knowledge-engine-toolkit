// Package decorators composes cross-cutting concerns — a per-op timeout and the
// observability trio (logging, metrics, tracing) — around any
// core/interfaces.ReplayBuffer, without touching the backend. It mirrors
// clients/lock/decorators and its ordering (ARCHITECTURE.md#decorator-order): the timeout
// nests closest to the backend and the observability trio wraps it as Tracing → Metrics →
// Logging (Tracing outermost, Logging innermost), so a span/metric covers the whole
// bounded operation. Every With* is nil-safe: a nil collaborator skips that decorator, so
// dev/test paths run undecorated.
package decorators

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// bufferDurationBuckets returns the histogram bucket boundaries (seconds) for
// replay-buffer op durations — sub-millisecond (memory) to a few hundred ms (a slow
// Redis round-trip).
func bufferDurationBuckets() []float64 {
	return []float64{
		.0005,
		.001,
		.0025,
		.005,
		.01,
		.025,
		.05,
		.1,
		.25,
		.5,
		1,
	}
}

// Builder composes decorators around a base ReplayBuffer using a fluent API.
type Builder struct {
	// base is the underlying buffer implementation to decorate.
	base interfaces.ReplayBuffer
	// logger drives the logging decorator; nil means no logging decorator.
	logger interfaces.Logger
	// metrics drives the metrics decorator; nil means no metrics decorator.
	metrics interfaces.Metrics
	// tracer drives the tracing decorator; nil means no tracing decorator.
	tracer interfaces.Tracer
	// name labels the buffer instance in metrics, spans, and logs.
	name string
	// timeout is the per-operation deadline; zero means no timeout decorator.
	timeout time.Duration
}

// NewBuilder creates a decorator builder wrapping base. name labels the buffer in
// metrics/spans/logs.
func NewBuilder(base interfaces.ReplayBuffer, name string) *Builder {
	return &Builder{base: base, name: name}
}

// WithTimeout adds a per-operation deadline; ops exceeding it are cancelled via context.
func (b *Builder) WithTimeout(d time.Duration) *Builder {
	b.timeout = d
	return b
}

// WithLogging adds a logging decorator: each op logs at Debug, failures at Error.
func (b *Builder) WithLogging(logger interfaces.Logger) *Builder {
	b.logger = logger
	return b
}

// WithMetrics adds a metrics decorator recording per-op counts (by outcome) and latency.
func (b *Builder) WithMetrics(m interfaces.Metrics) *Builder {
	b.metrics = m
	return b
}

// WithTracing adds a tracing decorator that opens a client span per op.
func (b *Builder) WithTracing(tracer interfaces.Tracer) *Builder {
	b.tracer = tracer
	return b
}

// Build constructs the decorated buffer, applied inside-out:
//
//	base → timeout → logging → metrics → tracing
//
// so the timeout nests closest to the backend and the observability trio wraps it as
// Tracing → Metrics → Logging (Logging innermost) per ARCHITECTURE.md#decorator-order. A
// nil collaborator skips its decorator, so an all-nil builder returns the base unchanged.
func (b *Builder) Build() interfaces.ReplayBuffer {
	rb := b.base

	if b.timeout > 0 {
		rb = &timeoutDecorator{inner: rb, timeout: b.timeout}
	}
	if b.logger != nil {
		rb = &loggingDecorator{inner: rb, logger: b.logger, name: b.name}
	}
	if b.metrics != nil {
		rb = newMetricsDecorator(rb, b.name, b.metrics)
	}
	if b.tracer != nil {
		rb = &tracingDecorator{inner: rb, tracer: b.tracer, name: b.name}
	}

	return rb
}
