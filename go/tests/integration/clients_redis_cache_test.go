//go:build integration

// This file verifies the Redis cache implementation against a real Redis instance
// (redis:7-alpine via testcontainers): the interfaces.ByteCache contract, the
// concrete extensions (GetOrLoad, TTLs, Ping, prefix/full invalidation), and how the
// cache degrades when its Redis is unreachable. The unit suite covers only
// construction (TLS wiring, defaults); every behaviour that needs a Redis runs here.
package integration

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	rediscache "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// RedisCacheSuite holds a real Redis container and a fresh Cache client per test.
type RedisCacheSuite struct {
	cache *rediscache.Cache
	testsuite.RedisIntegrationSuite
}

// TestRedisCacheSuite runs all Redis cache integration tests.
//
// Why this test is important:
//   - Without a top-level TestXxx function, `go test` ignores every suite method
//
// What it tests:
//   - Suite entrypoint wires RedisCacheSuite into the testify runner
func TestRedisCacheSuite(t *testing.T) {
	suite.Run(t, new(RedisCacheSuite))
}

func (s *RedisCacheSuite) SetupTest() {
	s.cache = s.newCache(nil)
}

func (s *RedisCacheSuite) TearDownTest() {
	if s.cache != nil {
		_ = s.cache.Close()
	}
}

// newCache builds a cache on the suite's Redis (DB 0, 2s op timeout, 5m default TTL),
// letting mutate adjust the config first.
func (s *RedisCacheSuite) newCache(mutate func(*rediscache.Config)) *rediscache.Cache {
	cfg := rediscache.DefaultConfig()
	cfg.Addr = s.RedisAddr
	cfg.OpTimeout = 2 * time.Second
	if mutate != nil {
		mutate(&cfg)
	}
	c := rediscache.New(&cfg)
	s.T().Cleanup(func() { _ = c.Close() })
	return c
}

// unreachableCache builds a cache whose address refuses connections (a port reserved
// and released), with mutate adjusting the config first.
func (s *RedisCacheSuite) unreachableCache(
	mutate func(*rediscache.Config),
) *rediscache.Cache {
	lis, err := (&net.ListenConfig{}).Listen(s.T().Context(), "tcp", "127.0.0.1:0")
	s.Require().NoError(err)
	addr := lis.Addr().String()
	s.Require().NoError(lis.Close())
	cfg := &rediscache.Config{Addr: addr, OpTimeout: time.Second}
	if mutate != nil {
		mutate(cfg)
	}
	c := rediscache.New(cfg)
	s.T().Cleanup(func() { _ = c.Close() })
	return c
}

// uniqueKey returns a test-scoped cache key to prevent cross-test pollution when
// tests share a single Redis instance.
func (s *RedisCacheSuite) uniqueKey(label string) string {
	return fmt.Sprintf("%s:%s", s.T().Name(), label)
}

// TestPing tests that Cache.Ping confirms the connection to the Redis server.
//
// Why this test is important:
//   - An early signal that the container address is wired correctly;
//     a failed Ping catches misconfiguration before any other test runs
//
// What it tests:
//   - Ping returns no error when Redis is reachable
func (s *RedisCacheSuite) TestPing() {
	err := s.cache.Ping(context.Background())
	s.Require().NoError(err, "Redis should be reachable")
}

// TestSetGet_Hit tests that a value written with Set is returned by Get.
//
// Why this test is important:
//   - This is the fundamental cache-aside happy path the repository decorator
//     relies on; a broken hit means every cached read falls through to the DB
//
// What it tests:
//   - Get returns (value, true) for a key previously written with Set
func (s *RedisCacheSuite) TestSetGet_Hit() {
	ctx := context.Background()
	key := s.uniqueKey("hit")
	want := []byte("hello-redis")

	s.cache.Set(ctx, key, want, time.Minute)

	got, ok := s.cache.Get(ctx, key)
	s.True(ok, "expected cache hit")
	s.Equal(want, got)
}

// TestGet_Miss tests that Get returns (nil, false) for a key that has never
// been written.
//
// Why this test is important:
//   - Callers use the bool to decide whether to invoke the loader; a wrong
//     return here breaks the cache-aside pattern and may surface stale data
//
// What it tests:
//   - Get on an unknown key returns (nil, false)
func (s *RedisCacheSuite) TestGet_Miss() {
	ctx := context.Background()
	key := s.uniqueKey("miss")

	got, ok := s.cache.Get(ctx, key)
	s.False(ok, "expected cache miss for unknown key")
	s.Nil(got)
}

// TestDelete tests that Delete removes a key so subsequent Get returns a miss.
//
// Why this test is important:
//   - This is the invalidation path exercised by repository writes and mutation
//     hooks; a failure means stale entries persist indefinitely
//
// What it tests:
//   - Key is present before Delete and absent after Delete
func (s *RedisCacheSuite) TestDelete() {
	ctx := context.Background()
	key := s.uniqueKey("del")

	s.cache.Set(ctx, key, []byte("to-be-deleted"), time.Minute)
	_, hit := s.cache.Get(ctx, key)
	s.Require().True(hit, "key should exist before Delete")

	s.cache.Delete(ctx, key)

	_, hit = s.cache.Get(ctx, key)
	s.False(hit, "key should be absent after Delete")
}

// TestGetOrLoad_PopulatesOnMiss tests that GetOrLoad invokes the loader on a
// cache miss, stores the result, and returns from cache on the second call.
//
// Why this test is important:
//   - Confirms the singleflight population path that prevents cache stampedes;
//     a loader called more than once indicates the result was not persisted
//
// What it tests:
//   - First call (miss): loader is invoked once and the result is returned
//   - Second call (hit): loader is not invoked again; cached value is returned
func (s *RedisCacheSuite) TestGetOrLoad_PopulatesOnMiss() {
	ctx := context.Background()
	key := s.uniqueKey("load")
	calls := 0

	loader := func() ([]byte, error) {
		calls++
		return []byte("loaded-value"), nil
	}

	// First call: miss, loader invoked.
	got, err := s.cache.GetOrLoad(ctx, key, loader, time.Minute)
	s.Require().NoError(err)
	s.Equal([]byte("loaded-value"), got)
	s.Equal(1, calls, "loader should be called exactly once on miss")

	// Second call: hit, loader not invoked again.
	got2, err := s.cache.GetOrLoad(ctx, key, loader, time.Minute)
	s.Require().NoError(err)
	s.Equal([]byte("loaded-value"), got2)
	s.Equal(1, calls, "loader should not be called again on cache hit")
}

// TestGetOrLoad_LoaderErrorAndNilValue tests GetOrLoad's two non-value loader
// outcomes: an error propagates without populating the cache, and a nil value
// passes through as nil.
//
// Why this test is important:
//   - Caching a failed load would serve error data to all subsequent callers.
//   - A loader may legitimately resolve "no value"; the cache must pass that nil
//     through cleanly rather than panicking on it.
//
// What it tests:
//   - A loader error is returned with a nil value and the key stays absent.
//   - A loader returning (nil, nil) makes GetOrLoad return (nil, nil).
func (s *RedisCacheSuite) TestGetOrLoad_LoaderErrorAndNilValue() {
	ctx := context.Background()
	errLoad := apperr.Sentinel("db connection failed")

	val, err := s.cache.GetOrLoad(ctx, s.uniqueKey("err"), func() ([]byte, error) {
		return nil, errLoad
	}, time.Minute)
	s.Require().ErrorIs(err, errLoad)
	s.Nil(val)
	_, ok := s.cache.Get(ctx, s.uniqueKey("err"))
	s.False(ok, "cache should not be populated after loader error")

	val, err = s.cache.GetOrLoad(ctx, s.uniqueKey("nil"), func() ([]byte, error) {
		return nil, nil
	}, 0)
	s.Require().NoError(err)
	s.Nil(val, "a nil loader result passes through as a nil value")
}

// TestTTLExpiry tests that a key written with a short TTL is unavailable after
// the TTL elapses.
//
// Why this test is important:
//   - TTL enforcement by Redis is the only mechanism preventing stale cache
//     entries; this cannot be validated with mocks or a fake clock
//
// What it tests:
//   - Key is present immediately after Set with a short TTL
//   - Key is absent after the TTL elapses
func (s *RedisCacheSuite) TestTTLExpiry() {
	ctx := context.Background()
	key := s.uniqueKey("ttl")
	ttl := 60 * time.Millisecond

	s.cache.Set(ctx, key, []byte("expires-soon"), ttl)

	_, hit := s.cache.Get(ctx, key)
	s.True(hit, "key should be present immediately after Set")

	time.Sleep(ttl + 100*time.Millisecond)

	_, hit = s.cache.Get(ctx, key)
	s.False(hit, "key should have expired after TTL elapsed")
}

// TestTTLSelection tests which TTL Set applies: an explicit TTL wins, a zero TTL
// takes the configured default, and a zero configured default falls back to 5m.
//
// Why this test is important:
//   - Callers passing zero expect the configured default; if zero meant "no
//     expiry", Redis memory would grow unbounded. An explicit TTL must override the
//     default so different data can expire on its own schedule.
//
// What it tests:
//   - Set with TTL=60s leaves a TTL in (50s, 60s].
//   - Set with TTL=0 on a DefaultTTL=30s cache leaves a TTL in (0, 30s].
//   - Set with TTL=0 on a DefaultTTL=0 cache leaves a TTL in (0, 5m] and the value
//     is readable.
func (s *RedisCacheSuite) TestTTLSelection() {
	ctx := context.Background()
	ttlOf := func(c *rediscache.Cache, key string) time.Duration {
		return c.Client().TTL(ctx, key).Val()
	}

	explicit := s.uniqueKey("explicit")
	s.cache.Set(ctx, explicit, []byte("v"), 60*time.Second)
	s.LessOrEqual(ttlOf(s.cache, explicit), 60*time.Second)
	s.Greater(ttlOf(s.cache, explicit), 50*time.Second, "TTL should be close to 60s")

	short := s.newCache(
		func(cfg *rediscache.Config) { cfg.DefaultTTL = 30 * time.Second },
	)
	configured := s.uniqueKey("configured")
	short.Set(ctx, configured, []byte("v"), 0)
	s.Greater(ttlOf(short, configured), time.Duration(0), "a zero TTL takes the default")
	s.LessOrEqual(ttlOf(short, configured), 30*time.Second)

	unset := s.newCache(func(cfg *rediscache.Config) { cfg.DefaultTTL = 0 })
	fallback := s.uniqueKey("fallback")
	unset.Set(ctx, fallback, []byte("v"), 0)
	s.Greater(ttlOf(unset, fallback), time.Duration(0), "New falls back to a 5m default")
	s.LessOrEqual(ttlOf(unset, fallback), 5*time.Minute)
	got, ok := unset.Get(ctx, fallback)
	s.True(ok)
	s.Equal([]byte("v"), got)
}

// TestZeroOpTimeout tests that a cache with OpTimeout=0 runs every op under the
// caller's context rather than an already-expired one.
//
// Why this test is important:
//   - With OpTimeout zero the cache must use the parent context unchanged; wrapping
//     it with a zero timeout would make every op deadline-exceed immediately.
//
// What it tests:
//   - Set → Get round-trips and Delete removes the key with OpTimeout=0.
func (s *RedisCacheSuite) TestZeroOpTimeout() {
	ctx := context.Background()
	c := s.newCache(func(cfg *rediscache.Config) { cfg.OpTimeout = 0 })
	key := s.uniqueKey("k")

	c.Set(ctx, key, []byte("value1"), time.Minute)
	got, ok := c.Get(ctx, key)
	s.Require().True(ok, "expected cache hit after Set with zero OpTimeout")
	s.Equal([]byte("value1"), got)

	c.Delete(ctx, key)
	_, ok = c.Get(ctx, key)
	s.False(ok, "expected cache miss after Delete with zero OpTimeout")
}

// TestInvalidatePrefix tests prefix-scoped invalidation: matching keys are removed,
// non-matching keys survive, glob metacharacters in the prefix are escaped, and an
// empty/whitespace prefix is a guarded no-op (never a full flush).
//
// Why this test is important:
//   - This is the cache-busting primitive an invalidation consumer calls; an
//     over-broad match would wipe unrelated keys on a shared Redis, and an unescaped
//     prefix turns a hostile tenant id into a wildcard.
//
// What it tests:
//   - Keys under the prefix are deleted; keys outside it (a different tenant and an
//     unrelated namespace) survive.
//   - A prefix containing a Redis glob metacharacter ("*") matches only the literal
//     key.
//   - An empty or whitespace-only prefix deletes nothing and returns nil.
func (s *RedisCacheSuite) TestInvalidatePrefix() {
	ctx := context.Background()
	p := s.uniqueKey("")
	set := func(keys ...string) {
		for _, k := range keys {
			s.cache.Set(ctx, p+k, []byte("v"), time.Minute)
		}
	}
	present := func(k string) bool {
		_, ok := s.cache.Get(ctx, p+k)
		return ok
	}

	set("ctx:t1:a", "ctx:t1:b", "ctx:t2:a", "other:key")
	s.Require().NoError(s.cache.InvalidatePrefix(ctx, p+"ctx:t1:"))
	s.False(present("ctx:t1:a"), "t1:a should be invalidated")
	s.False(present("ctx:t1:b"), "t1:b should be invalidated")
	s.True(present("ctx:t2:a"), "a different tenant must survive")
	s.True(present("other:key"), "an unrelated namespace must survive")

	// Unescaped, MATCH "lit*:*" would also match "litERAL:1"; escaping the "*"
	// restricts the match to the literal key.
	set("lit*:1", "litERAL:1")
	s.Require().NoError(s.cache.InvalidatePrefix(ctx, p+"lit*:"))
	s.False(present("lit*:1"), "literal-star key should be invalidated")
	s.True(present("litERAL:1"), "non-matching key must survive (metachar escaped)")

	set("k1")
	s.Require().NoError(s.cache.InvalidatePrefix(ctx, ""))
	s.Require().NoError(s.cache.InvalidatePrefix(ctx, "   "))
	s.True(present("k1"), "empty/whitespace prefix must not flush the cache")
}

// TestInvalidateAll tests that InvalidateAll clears the cache's database.
//
// Why this test is important:
//   - InvalidateAll completes the interfaces.CacheInvalidator contract; a no-op
//     would be a silent lie to any caller that relies on it.
//
// What it tests:
//   - On a cache bound to its own Redis DB, keys set before InvalidateAll are gone
//     after it.
func (s *RedisCacheSuite) TestInvalidateAll() {
	ctx := context.Background()
	c := s.newCache(
		func(cfg *rediscache.Config) { cfg.DB = 15 },
	) // FLUSHDB stays in DB 15
	c.Set(ctx, "k1", []byte("v1"), time.Minute)
	c.Set(ctx, "k2", []byte("v2"), time.Minute)

	s.Require().NoError(c.InvalidateAll(ctx))

	_, ok := c.Get(ctx, "k1")
	s.False(ok, "k1 should be cleared by InvalidateAll")
	_, ok = c.Get(ctx, "k2")
	s.False(ok, "k2 should be cleared by InvalidateAll")
}

// TestClose tests that Close releases the client without error.
//
// Why this test is important:
//   - Resource cleanup must complete; a broken Close leaks connections and file
//     descriptors.
//
// What it tests:
//   - Close on a connected cache returns nil.
func (s *RedisCacheSuite) TestClose() {
	c := rediscache.New(&rediscache.Config{Addr: s.RedisAddr})
	s.Require().NoError(c.Ping(context.Background()))
	s.Require().NoError(c.Close())
}

// TestUnreachable_ByteCacheDegradesToMiss tests the ByteCache methods when Redis
// refuses connections, in both failure modes and with a nil logger.
//
// Why this test is important:
//   - Cache unavailability must degrade to misses, never fail requests. In error
//     mode operators need the failure logged for alerting; with no logger the cache
//     must still not panic.
//
// What it tests:
//   - Bypass mode: Get returns (nil, false).
//   - Error mode: Get returns (nil, false), Set and Delete do not panic, and each
//     failure is logged at Error.
//   - Error mode with a nil logger: Get/Set/Delete return a miss and do not panic.
func (s *RedisCacheSuite) TestUnreachable_ByteCacheDegradesToMiss() {
	ctx := context.Background()

	bypass := s.unreachableCache(func(cfg *rediscache.Config) {
		cfg.FailureMode = rediscache.FailureModeBypass
	})
	val, ok := bypass.Get(ctx, "key1")
	s.False(ok, "expected miss when Redis is unavailable in bypass mode")
	s.Nil(val)

	spy := fixtures.NewSpyLogger()
	logged := s.unreachableCache(func(cfg *rediscache.Config) {
		cfg.FailureMode = rediscache.FailureModeError
		cfg.Logger = spy
	})
	val, ok = logged.Get(ctx, "key1")
	s.False(ok, "expected miss when Redis is unavailable")
	s.Nil(val)
	logged.Set(ctx, "key2", []byte("value2"), time.Minute)
	logged.Delete(ctx, "key3")
	s.Len(spy.ErrorCalls, 3, "error mode logs each failed Get/Set/Delete at Error")

	silent := s.unreachableCache(func(cfg *rediscache.Config) {
		cfg.FailureMode = rediscache.FailureModeError
		cfg.Logger = nil
	})
	val, ok = silent.Get(ctx, "any-key")
	s.False(ok, "expected miss when Redis is unavailable")
	s.Nil(val)
	silent.Set(ctx, "any-key", []byte("data"), time.Minute)
	silent.Delete(ctx, "any-key")
}

// TestUnreachable_InvalidationSurfacesErrors tests that the CacheInvalidator
// surface returns a transient-coded error when Redis is unreachable, rather than
// reporting success.
//
// Why this test is important:
//   - A swallowed invalidation error would leave a caller believing stale entries
//     were purged when they were not; the error must surface (coded Unavailable) so
//     the caller can retry.
//
// What it tests:
//   - InvalidatePrefix and InvalidateAll each return an error coded Unavailable.
func (s *RedisCacheSuite) TestUnreachable_InvalidationSurfacesErrors() {
	ctx := context.Background()
	c := s.unreachableCache(nil)

	prefixErr := c.InvalidatePrefix(ctx, "tenant:42:")
	s.Require().Error(prefixErr, "a scan failure must surface, not be swallowed")
	s.Equal(apperr.CodeUnavailable, apperr.Code(prefixErr))

	allErr := c.InvalidateAll(ctx)
	s.Require().Error(allErr, "a flushdb failure must surface")
	s.Equal(apperr.CodeUnavailable, apperr.Code(allErr))
}

// TestUnreachable_Lifecycle tests the cache's lifecycle contract: the pool is
// created eagerly (so Liveness passes) but connectivity is proven only by
// Start/Readiness, which fail against an unreachable server.
//
// Why this test is important:
//   - Liveness (pool exists) and Readiness (server reachable) must answer different
//     questions so Kubernetes restarts vs. deroutes correctly.
//
// What it tests:
//   - The cache is an interfaces.Client; Liveness passes; Start and Readiness error
//     against a refused address; Stop closes cleanly.
func (s *RedisCacheSuite) TestUnreachable_Lifecycle() {
	ctx := context.Background()
	lc, ok := any(s.unreachableCache(nil)).(interfaces.Client)
	s.Require().True(ok)

	s.Require().NoError(lc.Liveness(ctx), "the pool is constructed by New")
	s.Require().Error(lc.Start(ctx), "a refused address fails the Ping")
	s.Require().Error(lc.Readiness(ctx))
	s.NoError(lc.Stop(ctx))
}
