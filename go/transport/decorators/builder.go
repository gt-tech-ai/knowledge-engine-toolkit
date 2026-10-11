// Package decorators provides fluent decorator composition for transport
// handlers. Decorators add cross-cutting concerns (logging, metrics, recovery)
// around any HandlerFunc without modifying it.
package decorators

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
)

// defaultBuckets returns the default histogram bucket boundaries for handler
// execution durations (in seconds).
func defaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// HandlerBuilder constructs a decorated handler using the fluent API pattern.
// Each With* method enables a decorator; Build applies them inside-out:
// base → timeout → rate_limit → metrics → logging → recovery (recovery is outermost).
type HandlerBuilder[Req any, Resp any] struct {
	// logger is the structured logger for the logging and recovery decorators.
	// Nil means no logging (recovery still catches panics but won't log them).
	logger interfaces.Logger

	// metrics is the metrics provider for the metrics decorator. Nil means no metrics.
	metrics interfaces.Metrics

	// limiter is the rate limiter for request throttling. Nil means no rate limiting.
	limiter interfaces.RateLimiter

	// base is the underlying handler function to decorate.
	base transport.HandlerFunc[Req, Resp]

	// name is the handler name used for metric labels and log fields.
	name string

	// timeout is the per-request deadline. Zero means no timeout.
	timeout time.Duration

	// recovery enables panic recovery as the outermost decorator.
	recovery bool
}

// NewHandlerBuilder creates a new decorator builder wrapping the given base handler.
// The name is used as a label/prefix for all cross-cutting concerns.
func NewHandlerBuilder[Req, Resp any](
	base transport.HandlerFunc[Req, Resp],
	name string,
) *HandlerBuilder[Req, Resp] {
	return &HandlerBuilder[Req, Resp]{base: base, name: name}
}

// WithLogging adds a logging decorator that logs execution entry at Debug
// level and errors at Error level.
func (b *HandlerBuilder[Req, Resp]) WithLogging(
	logger interfaces.Logger,
) *HandlerBuilder[Req, Resp] {
	b.logger = logger
	return b
}

// WithMetrics adds a metrics decorator that records execution counts and
// durations via the interfaces.Metrics abstraction.
func (b *HandlerBuilder[Req, Resp]) WithMetrics(
	m interfaces.Metrics,
) *HandlerBuilder[Req, Resp] {
	b.metrics = m
	return b
}

// WithRecovery adds a recovery decorator that catches panics and converts
// them to CodeInternal errors. Recovery is always outermost when enabled.
func (b *HandlerBuilder[Req, Resp]) WithRecovery() *HandlerBuilder[Req, Resp] {
	b.recovery = true
	return b
}

// WithTimeout adds a per-request deadline. Requests that exceed the timeout
// are cancelled via context and return a deadline-exceeded error.
func (b *HandlerBuilder[Req, Resp]) WithTimeout(
	d time.Duration,
) *HandlerBuilder[Req, Resp] {
	b.timeout = d
	return b
}

// WithRateLimit adds a rate limiter that rejects requests when the rate limit
// is exceeded, returning a CodeUnavailable error.
func (b *HandlerBuilder[Req, Resp]) WithRateLimit(
	limiter interfaces.RateLimiter,
) *HandlerBuilder[Req, Resp] {
	b.limiter = limiter
	return b
}

// Build constructs the decorated handler. Decorators are applied inside-out:
// base → timeout → rate_limit → metrics → logging → recovery (recovery is outermost).
func (b *HandlerBuilder[Req, Resp]) Build() transport.HandlerFunc[Req, Resp] {
	h := b.base

	if b.timeout > 0 {
		h = wrapTimeout(h, b.timeout)
	}
	if b.limiter != nil {
		h = wrapRateLimit(h, b.limiter)
	}
	if b.metrics != nil {
		h = wrapMetrics(h, b.name, b.metrics)
	}
	if b.logger != nil {
		h = wrapLogging(h, b.name, b.logger)
	}
	if b.recovery {
		h = wrapRecovery(h, b.name, b.logger)
	}

	return h
}

// wrapLogging wraps a handler with structured logging.
func wrapLogging[Req, Resp any](
	inner transport.HandlerFunc[Req, Resp],
	name string,
	logger interfaces.Logger,
) transport.HandlerFunc[Req, Resp] {
	return func(ctx context.Context, req Req) (Resp, error) {
		logger.WithContext(ctx).Debug("handler.Execute", "handler", name)
		start := time.Now()
		resp, err := inner(ctx, req)
		duration := time.Since(start)

		if err != nil {
			logger.WithContext(ctx).Error(
				"handler.Execute failed",
				"handler", name,
				"duration", duration.String(),
				"error", err,
			)
		}
		return resp, err
	}
}

// wrapMetrics wraps a handler with metrics recording.
func wrapMetrics[Req, Resp any](
	inner transport.HandlerFunc[Req, Resp],
	name string,
	m interfaces.Metrics,
) transport.HandlerFunc[Req, Resp] {
	executions := m.Counter(
		"controller_executions_total",
		"Total controller handler executions",
		"handler",
	)
	errors := m.Counter(
		"controller_errors_total",
		"Total controller handler errors",
		"handler",
	)
	duration := m.Histogram(
		"controller_handle_duration_seconds",
		"Controller handler duration",
		defaultBuckets(),
		"handler",
	)

	return func(ctx context.Context, req Req) (Resp, error) {
		start := time.Now()
		resp, err := inner(ctx, req)
		executions.Inc(name)
		duration.Observe(time.Since(start).Seconds(), name)
		if err != nil {
			errors.Inc(name)
		}
		return resp, err
	}
}

// wrapRecovery wraps a handler with panic recovery. Panics are caught,
// logged (when a logger is available), and converted to CodeInternal errors.
func wrapRecovery[Req, Resp any](
	inner transport.HandlerFunc[Req, Resp],
	name string,
	logger interfaces.Logger,
) transport.HandlerFunc[Req, Resp] {
	return func(ctx context.Context, req Req) (resp Resp, err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				if logger != nil {
					logger.WithContext(ctx).Error(
						"handler panic recovered",
						"handler", name,
						"panic", fmt.Sprint(r),
						"stack", string(stack),
					)
				}
				err = apperr.Internal(fmt.Sprintf("panic recovered: %v", r))
			}
		}()
		return inner(ctx, req)
	}
}

// wrapTimeout wraps a handler with a per-request deadline.
func wrapTimeout[Req, Resp any](
	inner transport.HandlerFunc[Req, Resp],
	timeout time.Duration,
) transport.HandlerFunc[Req, Resp] {
	return func(ctx context.Context, req Req) (Resp, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return inner(ctx, req)
	}
}

// wrapRateLimit wraps a handler with rate limiting. Requests that exceed the
// rate limit are rejected with a CodeUnavailable error.
func wrapRateLimit[Req, Resp any](
	inner transport.HandlerFunc[Req, Resp],
	limiter interfaces.RateLimiter,
) transport.HandlerFunc[Req, Resp] {
	return func(ctx context.Context, req Req) (resp Resp, err error) {
		if !limiter.Allow() {
			return resp, apperr.Unavailable("rate limit exceeded")
		}
		return inner(ctx, req)
	}
}
