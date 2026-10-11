package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rediscache "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/redis"
)

// The Redis cache's behaviour against a server (reads, writes, TTLs, invalidation,
// and degradation when Redis is unreachable) is covered by the testcontainers suite
// in go/tests/integration/clients_redis_cache_test.go; these unit tests cover only
// construction, which does no I/O.

// TestRedisCache_TLSConfiguresEncryptedClient tests that Config.TLS makes the
// underlying redis client use an encrypted connection.
//
// Why this test is important:
//   - Managed Redis (AWS ElastiCache with an auth token) mandates in-transit
//     encryption; a plaintext client silently i/o-times-out and every cache op
//     bypasses. This guards the wiring that turns TLS on for staging/prod while
//     leaving local dev plaintext.
//
// What it tests:
//   - TLS:true  -> the client's Options().TLSConfig is non-nil
//   - TLS:false -> it is nil (local dev stays plaintext)
func TestRedisCache_TLSConfiguresEncryptedClient(t *testing.T) {
	t.Parallel()

	secure := rediscache.New(&rediscache.Config{Addr: "example:6379", TLS: true})
	require.NotNil(
		t,
		secure.Client().Options().TLSConfig,
		"TLS:true must configure an encrypted client",
	)

	plain := rediscache.New(&rediscache.Config{Addr: "example:6379"})
	require.Nil(
		t,
		plain.Client().Options().TLSConfig,
		"TLS:false must leave the client plaintext",
	)
}

// TestRedisCache_DefaultConfig tests that DefaultConfig returns sensible
// non-zero defaults for connecting to a local Redis instance.
//
// Why this test is important:
//   - Services that omit explicit cache config inherit these defaults; zero or
//     invalid defaults (e.g., empty Addr) would fail to connect silently
//
// What it tests:
//   - Addr is "localhost:6379", DefaultTTL is 5m, OpTimeout is 500ms, PoolSize >= 1
func TestRedisCache_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := rediscache.DefaultConfig()
	assert.Equal(t, "localhost:6379", cfg.Addr)
	assert.Equal(t, 5*time.Minute, cfg.DefaultTTL)
	assert.Equal(t, 500*time.Millisecond, cfg.OpTimeout)
	assert.GreaterOrEqual(t, cfg.PoolSize, 1)
}

// TestRedisCache_ClientAccessor tests that Client() returns a non-nil
// underlying Redis client.
//
// Why this test is important:
//   - Advanced callers (e.g., pipeline and subscription operations) bypass the
//     cache interface and use the raw client directly; a nil client panics on use
//
// What it tests:
//   - c.Client() returns a non-nil *redis.Client after construction
func TestRedisCache_ClientAccessor(t *testing.T) {
	t.Parallel()

	c := rediscache.New(&rediscache.Config{Addr: "example:6379"}) // New does no I/O
	client := c.Client()
	require.NotNil(t, client, "expected non-nil redis.Client from Client()")
}
