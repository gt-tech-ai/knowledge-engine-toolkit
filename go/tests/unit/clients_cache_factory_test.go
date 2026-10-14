package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TestCacheFactory_DefaultsToAMissingStubAndBuildsRedisOffline tests the cache
// factory's defaults and backends.
//
// Why this test is important:
//   - The graph must boot with no Redis: an empty config is the stub, which misses
//     every Get so callers fall through to their source of truth.
//   - The Redis client connects lazily, so a wiring regression must surface at
//     construction, with TLS plumbed through for managed Redis.
//
// What it tests:
//   - DefaultConfig is the stub kind with a 5-minute TTL and a 500ms op timeout.
//   - The empty config's cache misses a key just Set.
//   - The redis kind with TLS builds without dialing and, decorated, can still
//     invalidate.
func TestCacheFactory_DefaultsToAMissingStubAndBuildsRedisOffline(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	cfg := cache.DefaultConfig()
	assert.Equal(t, cache.Config{
		DefaultTTL: 5 * time.Minute,
		OpTimeout:  500 * time.Millisecond,
	}, cfg)

	stub, err := cache.NewFromConfig(ctx, cfg, cache.Deps{})
	require.NoError(t, err)
	stub.Set(ctx, "k", []byte("v"), 0)
	_, hit := stub.Get(ctx, "k")
	assert.False(t, hit)

	cfg.Kind = cache.KindRedis
	cfg.Redis.Addr = "localhost:6379"
	cfg.Redis.TLS = true
	redis, err := cache.NewFromConfig(ctx, cfg, cache.Deps{})
	require.NoError(t, err)
	_, invalidates := redis.(interfaces.CacheInvalidator)
	assert.True(t, invalidates, "the decorated redis cache keeps prefix invalidation")
}
