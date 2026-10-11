package repository

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	repodeco "github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository/decorators"
)

// Settings tunes the decorated-repository stack DecoratedFromConfig builds. Its
// values were per-app constants duplicated across every service's config wiring;
// centralizing them here gives the resilience policy a single source of
// truth that apps override only when a product choice demands it.
type Settings struct {
	// Timeout bounds a single decorated repository operation. Zero skips the
	// timeout decorator (leaving operation lifetime bounded by the caller's ctx).
	Timeout time.Duration

	// CacheTTL is how long a cached entity stays valid. Used only when Deps.Cache
	// is non-nil.
	CacheTTL time.Duration

	// CacheVersion namespaces cache keys so a schema change can invalidate them.
	CacheVersion int
}

// Deps carries the cross-cutting collaborators the stack wraps around the base
// repository. A nil resiliency collaborator (Retrier/Breaker) skips its decorator;
// caching is enabled only when Cache is non-nil. IDOf enables warm-on-create
// caching — nil caches lazily on Get.
type Deps[T any, ID comparable] struct {
	// Logger, when non-nil, adds the logging decorator to the stack.
	Logger interfaces.Logger
	// Metrics, when non-nil, adds the metrics decorator to the stack.
	Metrics interfaces.Metrics
	// Tracer, when non-nil, adds the tracing decorator to the stack.
	Tracer interfaces.Tracer
	// Retrier, when non-nil, adds the retry decorator to the stack.
	Retrier interfaces.Retrier
	// Breaker, when non-nil, adds the circuit-breaker decorator to the stack.
	Breaker interfaces.CircuitBreaker
	// Cache, when non-nil, enables the read-through caching decorator.
	Cache interfaces.ByteCache
	// IDOf derives the cache key from an entity, enabling warm-on-create caching;
	// nil leaves entities cached lazily on Get.
	IDOf func(*T) ID
}

// DecoratedFromConfig wraps a store in the canonical repository resilience +
// observability stack (circuit breaker, retry, timeout, logging, metrics, tracing,
// and optional caching), using the given settings and collaborators. It is the
// shared push-down of the per-app newDecoratedRepository /
// provide<X>Repository wiring that every service duplicated: an app now injects its
// store, a name, settings, and deps, and gets the fully decorated repository — the
// resilience policy lives here, not copy-pasted into each composition root.
func DecoratedFromConfig[T, P any, ID comparable](
	store interfaces.Store[T, P, ID],
	name string,
	settings Settings,
	deps Deps[T, ID],
) interfaces.DecoratedRepository[T, P, ID] {
	base := New(store, name)
	// Order preserved from the per-app newDecoratedRepository helpers this replaces
	// (circuit breaker → logging → metrics → retry → timeout → tracing → caching),
	// so the push-down is behavior-preserving.
	builder := repodeco.NewBuilder(base, name).
		WithCircuitBreaker(deps.Breaker).
		WithLogging(deps.Logger).
		WithMetrics(deps.Metrics).
		WithRetry(deps.Retrier)

	if settings.Timeout > 0 {
		builder.WithTimeout(settings.Timeout)
	}

	builder.WithTracing(deps.Tracer)

	if deps.Cache != nil {
		builder.
			WithCacheTTL(settings.CacheTTL).
			WithCacheVersion(settings.CacheVersion).
			WithCaching(deps.Cache)
		// IDOf enables warm-on-create; nil leaves entities cached lazily on Get.
		if deps.IDOf != nil {
			builder.WithCacheKeyFunc(deps.IDOf)
		}
	}

	return builder.Build()
}
