// Package decorators provides fluent decorator composition for repositories.
// Decorators add cross-cutting concerns (logging, tracing, metrics, caching)
// around any interfaces.Repository implementation without modifying it.
package decorators

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Builder constructs a decorated repository using the fluent API pattern.
// Each With* method enables a decorator; Build applies them inside-out:
// base -> caching -> retry -> circuitBreaker -> timeout -> logging -> metrics ->
// tracing (tracing is outermost).
type Builder[T any, P any, ID comparable] struct {
	// base is the underlying repository implementation to decorate.
	base interfaces.Repository[T, P, ID]

	// logger is the structured logger for the logging decorator. Nil means no logging.
	logger interfaces.Logger

	// tracer is the distributed tracer for the tracing decorator. Nil means no tracing.
	tracer interfaces.Tracer

	// metrics is the metrics provider for the metrics decorator. Nil means no metrics.
	metrics interfaces.Metrics

	// cache is the byte cache for the caching decorator. Nil means no caching.
	cache interfaces.ByteCache

	// retrier is the retry strategy for the retry decorator. Nil means no retry.
	retrier interfaces.Retrier

	// cb is the circuit breaker for the circuit breaker decorator. Nil means no CB.
	cb interfaces.CircuitBreaker

	// cacheKeyFunc extracts an entity's id so the caching decorator can warm the
	// cache on Create. Nil disables warm-on-create.
	cacheKeyFunc func(*T) ID

	// name is the repository name used for metric labels, log fields, and span names.
	name string

	// timeout is the per-operation timeout. Zero means no timeout decorator.
	timeout time.Duration

	// cacheTTL is the per-entry TTL for cached values. Zero means use ByteCache default.
	cacheTTL time.Duration

	// cacheVersion is the schema version stamped on each envelope. Entries with
	// a different version are treated as misses.
	cacheVersion int
}

// NewBuilder creates a new decorator builder wrapping the given base
// repository. The name is used as a label/prefix for all cross-cutting
// concerns.
func NewBuilder[T, P any, ID comparable](
	base interfaces.Repository[T, P, ID],
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

// WithTracing adds a tracing decorator that creates a span per repository
// operation.
func (b *Builder[T, P, ID]) WithTracing(tracer interfaces.Tracer) *Builder[T, P, ID] {
	b.tracer = tracer
	return b
}

// WithMetrics adds a metrics decorator that records operation counts and
// durations via the interfaces.Metrics abstraction.
func (b *Builder[T, P, ID]) WithMetrics(m interfaces.Metrics) *Builder[T, P, ID] {
	b.metrics = m
	return b
}

// WithCaching adds a caching decorator for cache-aside lookups on Get.
// Requires the cached type T to be JSON-serializable. Uses the foundation
// cache envelope for version-aware serialization.
func (b *Builder[T, P, ID]) WithCaching(c interfaces.ByteCache) *Builder[T, P, ID] {
	b.cache = c
	return b
}

// WithCacheTTL sets the per-entry TTL for cached values.
// Zero means use the ByteCache default.
func (b *Builder[T, P, ID]) WithCacheTTL(d time.Duration) *Builder[T, P, ID] {
	b.cacheTTL = d
	return b
}

// WithCacheVersion sets the schema version stamped on each envelope. Entries
// with a different version are treated as misses and silently evicted.
func (b *Builder[T, P, ID]) WithCacheVersion(v int) *Builder[T, P, ID] {
	b.cacheVersion = v
	return b
}

// WithCacheKeyFunc supplies an id-extractor so the caching decorator warms the
// cache with the entity returned by Create. Without it, a created entity is
// cached lazily on its first Get.
func (b *Builder[T, P, ID]) WithCacheKeyFunc(f func(*T) ID) *Builder[T, P, ID] {
	b.cacheKeyFunc = f
	return b
}

// WithRetry adds a retry decorator that retries transient failures using the
// provided Retrier strategy.
func (b *Builder[T, P, ID]) WithRetry(r interfaces.Retrier) *Builder[T, P, ID] {
	b.retrier = r
	return b
}

// WithCircuitBreaker adds a circuit breaker decorator that prevents cascading
// failures by failing fast when the circuit is open.
func (b *Builder[T, P, ID]) WithCircuitBreaker(
	cb interfaces.CircuitBreaker,
) *Builder[T, P, ID] {
	b.cb = cb
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
	// Repository is the embedded base repository whose methods are promoted as the
	// undecorated default; the builder wraps this with each decorator.
	interfaces.Repository[T, P, ID]
}

// Build constructs the decorated repository. Decorators are applied
// inside-out: base -> caching -> retry -> circuitBreaker -> timeout ->
// logging -> metrics -> tracing (tracing is outermost).
func (b *Builder[T, P, ID]) Build() interfaces.DecoratedRepository[T, P, ID] {
	var repo interfaces.DecoratedRepository[T, P, ID] = &defaultDecoratedImpl[T, P, ID]{
		Repository: b.base,
	}

	if b.cache != nil {
		repo = &cachingDecorator[T, P, ID]{
			inner:   repo,
			cache:   b.cache,
			name:    b.name,
			ttl:     b.cacheTTL,
			version: b.cacheVersion,
			idOf:    b.cacheKeyFunc,
		}
	}
	if b.retrier != nil {
		repo = &retryDecorator[T, P, ID]{inner: repo, retrier: b.retrier}
	}
	if b.cb != nil {
		repo = &circuitBreakerDecorator[T, P, ID]{inner: repo, cb: b.cb}
	}
	if b.timeout > 0 {
		repo = &timeoutDecorator[T, P, ID]{inner: repo, timeout: b.timeout}
	}
	// Observability trio, innermost → outermost: Logging → Metrics → Tracing, so the
	// composed nesting is Tracing → Metrics → Logging (Logging innermost) per
	// ARCHITECTURE.md#decorator-order.
	if b.logger != nil {
		repo = &loggingDecorator[T, P, ID]{inner: repo, logger: b.logger, name: b.name}
	}
	if b.metrics != nil {
		repo = newMetricsDecorator(repo, b.name, b.metrics)
	}
	if b.tracer != nil {
		repo = &tracingDecorator[T, P, ID]{inner: repo, tracer: b.tracer, name: b.name}
	}

	return repo
}
