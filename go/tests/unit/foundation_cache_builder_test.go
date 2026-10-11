package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestCacheBuilder_DefaultConfig tests that the cache factory provides sensible
// production defaults.
//
// Why this test is important:
//   - Services that omit explicit cache config inherit these defaults; a zero
//     TTL would cause every cache entry to expire immediately, defeating caching
//
// What it tests:
//   - Default kind is KindRedis
//   - Default TTL is non-zero
func TestCacheBuilder_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := cache.DefaultConfig()
	assert.Equal(t, cache.KindRedis, cfg.Kind, "default kind should be KindRedis")
	assert.NotZero(t, cfg.DefaultTTL, "default TTL must be non-zero")
}

// TestCacheBuilder_UnknownKindReturnsError tests that the cache factory rejects
// unsupported cache backends.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup; a nil cache returned
//     silently would panic on the first Get/Set in production
//
// What it tests:
//   - cache.New(Kind(999)) returns a non-nil error
func TestCacheBuilder_UnknownKindReturnsError(t *testing.T) {
	t.Parallel()

	_, err := cache.New(cache.Kind(999))
	require.Error(t, err, "expected error for unknown kind")
}

// TestDecoratorBuilder_NoDecorators tests that the decorator builder returns the
// base cache unchanged when no decorators are applied.
//
// Why this test is important:
//   - In environments where metrics and logging are disabled, unnecessary wrapping
//     adds call overhead with no benefit; the builder must be a pass-through when empty
//
// What it tests:
//   - NewBuilder(base).Build() returns the exact same instance as base
func TestDecoratorBuilder_NoDecorators(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCache := mocks.NewMockByteCache(ctrl)

	result := decorators.NewBuilder(mockCache, "test").Build()
	assert.Same(
		t,
		mockCache,
		result,
		"builder with no decorators should return the base cache unchanged",
	)
}

// TestDecoratorBuilder_WithMetrics tests that the metrics decorator wraps the
// base cache with instrumentation.
//
// Why this test is important:
//   - Cache hit/miss metrics drive alerting on cache degradation; if the
//     decorator fails to register metrics, operators lose visibility into cache health
//
// What it tests:
//   - WithMetrics calls Counter and Histogram on the Metrics interface during build
//   - The decorated cache is a different instance from the base cache
func TestDecoratorBuilder_WithMetrics(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCache := mocks.NewMockByteCache(ctrl)
	mockMetrics := mocks.NewMockMetrics(ctrl)
	mockHits := mocks.NewMockCounter(ctrl)
	mockMisses := mocks.NewMockCounter(ctrl)
	mockDuration := mocks.NewMockHistogram(ctrl)

	// Expect the builder to create counter and histogram via the Metrics interface.
	mockMetrics.EXPECT().
		Counter("cache_hits_total", "Total cache hits", "cache").
		Return(mockHits)
	mockMetrics.EXPECT().
		Counter("cache_misses_total", "Total cache misses", "cache").
		Return(mockMisses)
	mockMetrics.EXPECT().
		Histogram(
			"cache_operation_duration_seconds", "Cache operation duration", gomock.Any(), "cache",
		).
		Return(mockDuration)

	decorated := decorators.NewBuilder(mockCache, "test").WithMetrics(mockMetrics).Build()

	assert.NotSame(t, mockCache, decorated, "decorated cache should differ from base")
}

// TestDecoratorBuilder_MetricsRecordsHit tests that the metrics decorator
// increments hit counters and records duration on cache hits.
//
// Why this test is important:
//   - Cache hit rate is a key performance indicator; uncounted hits make
//     dashboards under-report cache effectiveness and trigger false alarms
//
// What it tests:
//   - Get on a hit increments the hit counter and observes duration
func TestDecoratorBuilder_MetricsRecordsHit(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCache := mocks.NewMockByteCache(ctrl)
	mockMetrics := mocks.NewMockMetrics(ctrl)
	mockHits := mocks.NewMockCounter(ctrl)
	mockMisses := mocks.NewMockCounter(ctrl)
	mockDuration := mocks.NewMockHistogram(ctrl)

	mockMetrics.EXPECT().
		Counter("cache_hits_total", gomock.Any(), "cache").
		Return(mockHits)
	mockMetrics.EXPECT().
		Counter("cache_misses_total", gomock.Any(), "cache").
		Return(mockMisses)
	mockMetrics.EXPECT().
		Histogram("cache_operation_duration_seconds", gomock.Any(), gomock.Any(), "cache").
		Return(mockDuration)

	ctx := context.Background()

	// Cache returns a hit.
	mockCache.EXPECT().Get(ctx, "key1").Return([]byte("val"), true)

	// Expect: duration observed, hits incremented with cache name.
	mockDuration.EXPECT().Observe(gomock.Any(), "test-cache")
	mockHits.EXPECT().Inc("test-cache")

	decorated := decorators.NewBuilder(mockCache, "test-cache").
		WithMetrics(mockMetrics).
		Build()

	val, ok := decorated.Get(ctx, "key1")
	assert.True(t, ok, "expected cache hit")
	assert.Equal(t, "val", string(val))
}

// TestDecoratorBuilder_MetricsRecordsMiss tests that the metrics decorator
// increments miss counters on cache misses.
//
// Why this test is important:
//   - Cache miss rate drives capacity planning; under-reported misses hide
//     degradation caused by key eviction or misconfigured TTLs
//
// What it tests:
//   - Get on a miss increments the miss counter and observes duration
func TestDecoratorBuilder_MetricsRecordsMiss(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCache := mocks.NewMockByteCache(ctrl)
	mockMetrics := mocks.NewMockMetrics(ctrl)
	mockHits := mocks.NewMockCounter(ctrl)
	mockMisses := mocks.NewMockCounter(ctrl)
	mockDuration := mocks.NewMockHistogram(ctrl)

	mockMetrics.EXPECT().
		Counter("cache_hits_total", gomock.Any(), "cache").
		Return(mockHits)
	mockMetrics.EXPECT().
		Counter("cache_misses_total", gomock.Any(), "cache").
		Return(mockMisses)
	mockMetrics.EXPECT().
		Histogram("cache_operation_duration_seconds", gomock.Any(), gomock.Any(), "cache").
		Return(mockDuration)

	ctx := context.Background()

	// Cache returns a miss.
	mockCache.EXPECT().Get(ctx, "missing").Return([]byte(nil), false)

	// Expect: duration observed, misses incremented with cache name.
	mockDuration.EXPECT().Observe(gomock.Any(), "my-cache")
	mockMisses.EXPECT().Inc("my-cache")

	decorated := decorators.NewBuilder(mockCache, "my-cache").
		WithMetrics(mockMetrics).
		Build()

	_, ok := decorated.Get(ctx, "missing")
	assert.False(t, ok, "expected cache miss")
}

// TestDecoratorBuilder_MetricsRecordsSetAndDeleteDuration tests that Set and
// Delete operations record duration metrics.
//
// Why this test is important:
//   - Write and delete latency spikes indicate Redis connectivity issues or
//     large-value serialization problems; without metrics these go undetected
//
// What it tests:
//   - Set observes duration once; Delete observes duration once
func TestDecoratorBuilder_MetricsRecordsSetAndDeleteDuration(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockCache := mocks.NewMockByteCache(ctrl)
	mockMetrics := mocks.NewMockMetrics(ctrl)
	mockHits := mocks.NewMockCounter(ctrl)
	mockMisses := mocks.NewMockCounter(ctrl)
	mockDuration := mocks.NewMockHistogram(ctrl)

	mockMetrics.EXPECT().
		Counter("cache_hits_total", gomock.Any(), "cache").
		Return(mockHits)
	mockMetrics.EXPECT().
		Counter("cache_misses_total", gomock.Any(), "cache").
		Return(mockMisses)
	mockMetrics.EXPECT().
		Histogram("cache_operation_duration_seconds", gomock.Any(), gomock.Any(), "cache").
		Return(mockDuration)

	ctx := context.Background()

	mockCache.EXPECT().Set(ctx, "k", []byte("v"), 5*time.Second)
	mockCache.EXPECT().Delete(ctx, "k")

	// Expect duration observed for both Set and Delete.
	mockDuration.EXPECT().Observe(gomock.Any(), "dur-cache").Times(2)

	decorated := decorators.NewBuilder(mockCache, "dur-cache").
		WithMetrics(mockMetrics).
		Build()

	decorated.Set(ctx, "k", []byte("v"), 5*time.Second)
	decorated.Delete(ctx, "k")
}
