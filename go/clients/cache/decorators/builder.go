// Package decorators provides cache decorators for cross-cutting concerns.
package decorators

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.ByteCache = (*metricsDecorator)(nil)

// defaultBuckets returns the default histogram bucket boundaries for cache
// operation durations (in seconds). Matches Prometheus DefBuckets.
func defaultBuckets() []float64 {
	return []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// Builder composes decorators around a base ByteCache.
type Builder struct {
	// base is the underlying cache implementation to decorate.
	base interfaces.ByteCache

	// metrics is the metrics provider for registering counters and histograms.
	// Nil means no metrics decorator is applied.
	metrics interfaces.Metrics

	// cb is the circuit breaker for graceful degradation when the cache backend
	// is unavailable. Nil means no circuit breaking.
	cb interfaces.CircuitBreaker

	// name is the cache instance name used as a label in metrics.
	name string

	// timeout is the per-operation deadline. Zero means no timeout.
	timeout time.Duration
}

// NewBuilder creates a new decorator builder wrapping the given base cache.
func NewBuilder(base interfaces.ByteCache, name string) *Builder {
	return &Builder{base: base, name: name}
}

// WithMetrics adds a metrics decorator that records hits, misses, and
// operation durations via the interfaces.Metrics abstraction.
func (b *Builder) WithMetrics(m interfaces.Metrics) *Builder {
	b.metrics = m
	return b
}

// WithCircuitBreaker adds a circuit breaker that gracefully degrades cache
// operations when the cache backend is unavailable. When the circuit is open,
// Get returns (nil, false) and Set/Delete become no-ops.
func (b *Builder) WithCircuitBreaker(cb interfaces.CircuitBreaker) *Builder {
	b.cb = cb
	return b
}

// WithTimeout adds a per-operation deadline. Operations that exceed the
// timeout are cancelled via context.
func (b *Builder) WithTimeout(d time.Duration) *Builder {
	b.timeout = d
	return b
}

// Build constructs the decorated cache. Decorators are applied inside-out:
// base → circuit_breaker → timeout → metrics (metrics is outermost).
func (b *Builder) Build() interfaces.ByteCache {
	c := b.base

	if b.cb != nil {
		c = &circuitBreakerDecorator{inner: c, cb: b.cb}
	}
	if b.timeout > 0 {
		c = &timeoutDecorator{inner: c, timeout: b.timeout}
	}
	if b.metrics != nil {
		c = &metricsDecorator{
			inner: c,
			name:  b.name,
			hits:  b.metrics.Counter("cache_hits_total", "Total cache hits", "cache"),
			misses: b.metrics.Counter(
				"cache_misses_total",
				"Total cache misses",
				"cache",
			),
			duration: b.metrics.Histogram(
				"cache_operation_duration_seconds",
				"Cache operation duration",
				defaultBuckets(),
				"cache",
			),
		}
	}

	return c
}

// metricsDecorator wraps a ByteCache with metrics for hits, misses, and
// latency using the core interfaces.Counter and interfaces.Histogram
// abstractions.
type metricsDecorator struct {
	// inner is the wrapped cache implementation that handles actual storage.
	inner interfaces.ByteCache

	// hits counts the total number of cache hits (key found).
	hits interfaces.Counter

	// misses counts the total number of cache misses (key not found or error).
	misses interfaces.Counter

	// duration records operation latency in seconds for Get, Set, and Delete.
	duration interfaces.Histogram

	// name is the cache instance name used as a label dimension in all metrics.
	name string
}

// Get retrieves a value and records hit/miss metrics.
func (d *metricsDecorator) Get(ctx context.Context, key string) ([]byte, bool) {
	start := time.Now()
	val, ok := d.inner.Get(ctx, key)
	d.duration.Observe(time.Since(start).Seconds(), d.name)

	if ok {
		d.hits.Inc(d.name)
	} else {
		d.misses.Inc(d.name)
	}
	return val, ok
}

// Set stores a value and records duration.
func (d *metricsDecorator) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) {
	start := time.Now()
	d.inner.Set(ctx, key, value, ttl)
	d.duration.Observe(time.Since(start).Seconds(), d.name)
}

// Delete removes a key and records duration.
func (d *metricsDecorator) Delete(ctx context.Context, key string) {
	start := time.Now()
	d.inner.Delete(ctx, key)
	d.duration.Observe(time.Since(start).Seconds(), d.name)
}

// ---------------------------------------------------------------------------
// circuitBreakerDecorator
// ---------------------------------------------------------------------------

// Compile-time interface assertion.
var _ interfaces.ByteCache = (*circuitBreakerDecorator)(nil)

// circuitBreakerDecorator wraps cache operations with a circuit breaker for
// graceful degradation. When the circuit is open, Get returns (nil, false)
// and Set/Delete become no-ops — the application continues without caching.
type circuitBreakerDecorator struct {
	// inner is the wrapped cache whose operations run through the breaker.
	inner interfaces.ByteCache

	// cb gates each operation, short-circuiting calls when the circuit is open.
	cb interfaces.CircuitBreaker
}

// Get retrieves a value through the circuit breaker. On circuit open,
// returns (nil, false) for graceful degradation.
func (d *circuitBreakerDecorator) Get(ctx context.Context, key string) ([]byte, bool) {
	var val []byte
	var ok bool
	err := d.cb.Execute(func() error {
		val, ok = d.inner.Get(ctx, key)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		return nil, false
	}
	return val, ok
}

// Set stores a value through the circuit breaker. On circuit open,
// the operation is silently skipped.
func (d *circuitBreakerDecorator) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) {
	_ = d.cb.Execute(func() error {
		d.inner.Set(ctx, key, value, ttl)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	})
}

// Delete removes a key through the circuit breaker. On circuit open,
// the operation is silently skipped.
func (d *circuitBreakerDecorator) Delete(ctx context.Context, key string) {
	_ = d.cb.Execute(func() error {
		d.inner.Delete(ctx, key)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// timeoutDecorator
// ---------------------------------------------------------------------------

// Compile-time interface assertion.
var _ interfaces.ByteCache = (*timeoutDecorator)(nil)

// timeoutDecorator enforces a per-operation deadline by wrapping the context
// with a timeout.
type timeoutDecorator struct {
	// inner is the wrapped cache whose operations receive the bounded context.
	inner interfaces.ByteCache

	// timeout is the per-operation deadline applied to each call.
	timeout time.Duration
}

// Get retrieves a value with a deadline-bounded context.
func (d *timeoutDecorator) Get(ctx context.Context, key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.inner.Get(ctx, key)
}

// Set stores a value with a deadline-bounded context.
func (d *timeoutDecorator) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	d.inner.Set(ctx, key, value, ttl)
}

// Delete removes a key with a deadline-bounded context.
func (d *timeoutDecorator) Delete(ctx context.Context, key string) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	d.inner.Delete(ctx, key)
}
