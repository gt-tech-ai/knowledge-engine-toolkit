// Package decorators provides fluent decorator composition for services.
// Decorators add cross-cutting concerns (logging, authorization, panic
// recovery) around any interfaces.Service implementation without modifying it.
package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Builder constructs a decorated service using the fluent API pattern. Each
// With* method enables a decorator; Build applies them inside-out: base →
// authorization → logging → recovery (recovery is ALWAYS outermost).
type Builder[T any, P any, ID comparable] struct {
	// base is the underlying service implementation to decorate.
	base interfaces.Service[T, P, ID]

	// logger is the structured logger for the logging and recovery decorators.
	// Nil means no logging (recovery still catches panics but won't log them).
	logger interfaces.Logger

	// tracer is the distributed tracer for the tracing decorator. Nil = no tracing.
	tracer interfaces.Tracer

	// metrics is the metrics provider for the metrics decorator. Nil = no metrics.
	metrics interfaces.Metrics

	// validate is the business-rule validation function run before Create/Update.
	// Nil means no validation decorator.
	validate func(*T) error

	// authFn is the authorization check called before each operation. It
	// receives the context and an action string (e.g., "users.Get"). Nil means
	// no authorization checks.
	authFn func(ctx context.Context, action string) error

	// name is the service name used for log fields and authorization action
	// prefixes.
	name string

	// timeout is the per-operation timeout. Zero means no timeout decorator.
	timeout time.Duration
}

// NewBuilder creates a new service decorator builder wrapping the given base
// service. The name is used as a label/prefix for all cross-cutting concerns.
func NewBuilder[T, P any, ID comparable](
	base interfaces.Service[T, P, ID],
	name string,
) *Builder[T, P, ID] {
	return &Builder[T, P, ID]{base: base, name: name}
}

// WithLogging adds a logging decorator that logs operation entry at Debug
// level and errors at Error level.
func (b *Builder[T, P, ID]) WithLogging(logger interfaces.Logger) *Builder[T, P, ID] {
	b.logger = logger
	return b
}

// WithTracing adds a tracing decorator that creates a span per service
// operation, so the interceptor -> service -> repository cascade is visible in Tempo.
func (b *Builder[T, P, ID]) WithTracing(tracer interfaces.Tracer) *Builder[T, P, ID] {
	b.tracer = tracer
	return b
}

// WithValidation adds a business-rule validation decorator that runs fn against the
// entity before Create/Update and short-circuits on a non-nil (coded) error. fn
// should return a core/errors.AppError with CodeInvalidInput on violation.
func (b *Builder[T, P, ID]) WithValidation(fn func(*T) error) *Builder[T, P, ID] {
	b.validate = fn
	return b
}

// WithMetrics adds a metrics decorator that records per-operation counts,
// errors, and latency via the interfaces.Metrics abstraction (parity with the
// repository builder's WithMetrics).
func (b *Builder[T, P, ID]) WithMetrics(m interfaces.Metrics) *Builder[T, P, ID] {
	b.metrics = m
	return b
}

// WithAuthorization adds an authorization check before each service operation.
// The authFn receives the request context and an action string formatted as
// "<service>.<Operation>" (e.g., "users.Create"). Returning a non-nil error
// short-circuits the operation.
func (b *Builder[T, P, ID]) WithAuthorization(
	fn func(ctx context.Context, action string) error,
) *Builder[T, P, ID] {
	b.authFn = fn
	return b
}

// WithTimeout adds a timeout decorator that enforces a per-operation deadline.
func (b *Builder[T, P, ID]) WithTimeout(d time.Duration) *Builder[T, P, ID] {
	b.timeout = d
	return b
}

// defaultDecoratedImpl is a basic implementation of DecoratedRepository
// that serves as the base for the builder.
type defaultDecoratedImpl[T any, P any, ID comparable] struct {
	// Service is the embedded base service whose methods are promoted as the
	// undecorated default; the builder wraps this with each decorator.
	interfaces.Service[T, P, ID]
}

// Build constructs the decorated service. Decorators are applied inside-out:
// base → timeout → authorization → logging → metrics → tracing → validation → recovery
// (recovery is ALWAYS outermost; validation is the outermost business decorator). The
// observability trio nests Tracing → Metrics → Logging (Logging innermost), matching the
// client stack (ARCHITECTURE.md#decorator-order); timeout and authorization sit inside
// the trio.
func (b *Builder[T, P, ID]) Build() interfaces.DecoratedService[T, P, ID] {
	var svc interfaces.DecoratedService[T, P, ID] = &defaultDecoratedImpl[T, P, ID]{
		Service: b.base,
	}

	if b.timeout > 0 {
		svc = &timeoutDecorator[T, P, ID]{inner: svc, timeout: b.timeout}
	}
	if b.authFn != nil {
		svc = &authDecorator[T, P, ID]{inner: svc, authFn: b.authFn, name: b.name}
	}
	// Observability trio, innermost → outermost: Logging → Metrics → Tracing, so
	// the composed nesting is Tracing → Metrics → Logging (Logging innermost).
	if b.logger != nil {
		svc = &loggingDecorator[T, P, ID]{inner: svc, logger: b.logger, name: b.name}
	}
	if b.metrics != nil {
		svc = newMetricsDecorator(svc, b.name, b.metrics)
	}
	if b.tracer != nil {
		svc = &tracingDecorator[T, P, ID]{inner: svc, tracer: b.tracer, name: b.name}
	}
	// Validation is the outermost business decorator — it fails an invalid write fast,
	// just inside recovery.
	if b.validate != nil {
		svc = &validationDecorator[T, P, ID]{inner: svc, validate: b.validate}
	}

	// Recovery is ALWAYS outermost — catches panics from all inner decorators.
	svc = &recoveryDecorator[T, P, ID]{inner: svc, logger: b.logger, name: b.name}

	return svc
}
