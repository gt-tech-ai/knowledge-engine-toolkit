package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache"
)

// ---------------------------------------------------------------------------
// Cache Factory - Kind string representation
// ---------------------------------------------------------------------------

// TestCacheFactory_KindString tests the string representation of cache Kind
// values, covering all switch branches.
//
// Why this test is important:
//   - Kind.String() appears in error messages and logs when cache initialization
//     fails; incorrect strings make debugging harder
//
// What it tests:
//   - KindRedis -> "redis"
//   - Unknown Kind -> "Kind(N)" format
func TestCacheFactory_KindString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string
		kind cache.Kind
	}{
		{"redis", cache.KindRedis},
		{"Kind(99)", cache.Kind(99)},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.kind.String(), "Kind(%d).String()", tt.kind)
	}
}

// ---------------------------------------------------------------------------
// Cache Factory - DefaultConfig
// ---------------------------------------------------------------------------

// TestCacheFactory_DefaultConfig tests that DefaultConfig returns production-
// ready defaults.
//
// Why this test is important:
//   - Services that omit explicit cache config inherit these defaults; wrong
//     defaults (e.g., zero TTL) would cause Redis memory exhaustion or
//     immediate expiry
//
// What it tests:
//   - Kind is KindRedis
//   - DefaultTTL is 5 minutes
//   - OpTimeout is 500ms
func TestCacheFactory_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := cache.DefaultConfig()
	assert.Equal(t, cache.KindRedis, cfg.Kind)
	assert.Equal(t, 5*time.Minute, cfg.DefaultTTL)
	assert.Equal(t, 500*time.Millisecond, cfg.OpTimeout)
}

// ---------------------------------------------------------------------------
// Cache Factory - ConfigToOptions roundtrip
// ---------------------------------------------------------------------------

// TestCacheFactory_ConfigToOptions tests that Config.ToOptions produces a
// functional option slice that reproduces the original config.
//
// Why this test is important:
//   - ToOptions is used for config round-tripping; if the resulting options
//     don't reproduce the original config, services start with wrong settings
//
// What it tests:
//   - DefaultConfig.ToOptions applied to a blank config reproduces the original
func TestCacheFactory_ConfigToOptions(t *testing.T) {
	t.Parallel()

	original := cache.DefaultConfig()
	opts := original.ToOptions()

	var rebuilt cache.Config
	for _, opt := range opts {
		opt(&rebuilt)
	}

	assert.Equal(t, original.Kind, rebuilt.Kind)
	assert.Equal(t, original.DefaultTTL, rebuilt.DefaultTTL)
	assert.Equal(t, original.OpTimeout, rebuilt.OpTimeout)
}

// ---------------------------------------------------------------------------
// Cache Factory - With* option functions
// ---------------------------------------------------------------------------

// TestCacheFactory_WithDefaultTTL tests that WithDefaultTTL writes the
// supplied TTL into Config.DefaultTTL.
//
// Why this test is important:
//   - DefaultTTL is the expiry applied when callers pass zero to Set; a broken
//     option would silently fall back to a different lifetime, causing premature
//     eviction or unbounded Redis memory growth
//
// What it tests:
//   - WithDefaultTTL(10m) sets Config.DefaultTTL to 10 minutes
func TestCacheFactory_WithDefaultTTL(t *testing.T) {
	t.Parallel()

	var cfg cache.Config
	cache.WithDefaultTTL(10 * time.Minute)(&cfg)
	assert.Equal(t, 10*time.Minute, cfg.DefaultTTL)
}

// TestCacheFactory_WithRedisAddr tests that WithRedisAddr writes the supplied
// host:port into Config.Redis.Addr.
//
// Why this test is important:
//   - The address is what the cache client dials; a mis-wired option would point
//     production traffic at the wrong (or zero-value) Redis endpoint
//
// What it tests:
//   - WithRedisAddr("redis.prod:6380") sets Config.Redis.Addr to that value
func TestCacheFactory_WithRedisAddr(t *testing.T) {
	t.Parallel()

	var cfg cache.Config
	cache.WithRedisAddr("redis.prod:6380")(&cfg)
	assert.Equal(t, "redis.prod:6380", cfg.Redis.Addr)
}

// TestCacheFactory_WithRedisPassword tests that WithRedisPassword writes the
// supplied credential into Config.Redis.Password.
//
// Why this test is important:
//   - The password authenticates the cache client to Redis; if the option fails
//     to set it, connections to a password-protected instance are rejected and
//     caching silently degrades
//
// What it tests:
//   - WithRedisPassword("s3cr3t") sets Config.Redis.Password to that value
func TestCacheFactory_WithRedisPassword(t *testing.T) {
	t.Parallel()

	var cfg cache.Config
	cache.WithRedisPassword("s3cr3t")(&cfg)
	assert.Equal(t, "s3cr3t", cfg.Redis.Password)
}

// TestCacheFactory_WithRedisDB tests that WithRedisDB writes the supplied
// database index into Config.Redis.DB.
//
// Why this test is important:
//   - The DB number selects which logical Redis namespace is used; a wrong value
//     would read and write the wrong keyspace, mixing or losing cached data
//
// What it tests:
//   - WithRedisDB(3) sets Config.Redis.DB to 3
func TestCacheFactory_WithRedisDB(t *testing.T) {
	t.Parallel()

	var cfg cache.Config
	cache.WithRedisDB(3)(&cfg)
	assert.Equal(t, 3, cfg.Redis.DB)
}

// TestCacheFactory_WithRedisPoolSize tests that WithRedisPoolSize writes the
// supplied size into Config.Redis.PoolSize.
//
// Why this test is important:
//   - Pool size caps concurrent Redis connections; if the option is ignored the
//     cache runs on the default pool, which can throttle throughput under load
//
// What it tests:
//   - WithRedisPoolSize(50) sets Config.Redis.PoolSize to 50
func TestCacheFactory_WithRedisPoolSize(t *testing.T) {
	t.Parallel()

	var cfg cache.Config
	cache.WithRedisPoolSize(50)(&cfg)
	assert.Equal(t, 50, cfg.Redis.PoolSize)
}

// TestCacheFactory_WithRedisFailureMode tests that WithRedisFailureMode writes
// the supplied mode into Config.Redis.FailureMode.
//
// Why this test is important:
//   - FailureMode decides whether cache errors are logged (1=Error) or silently
//     absorbed (0=Bypass); a mis-wired option would hide cache failures from
//     operators or, conversely, flood logs with absorbed errors
//
// What it tests:
//   - WithRedisFailureMode(1) sets Config.Redis.FailureMode to 1
func TestCacheFactory_WithRedisFailureMode(t *testing.T) {
	t.Parallel()

	var cfg cache.Config
	cache.WithRedisFailureMode(1)(&cfg)
	assert.Equal(t, 1, cfg.Redis.FailureMode)
}

// TestCacheFactory_WithOpTimeout tests that WithOpTimeout writes the supplied
// deadline into Config.OpTimeout.
//
// Why this test is important:
//   - OpTimeout bounds each cache command so a slow Redis cannot stall request
//     handlers; if the option is ignored, the per-operation deadline is lost and
//     a hung cache can block the caller indefinitely
//
// What it tests:
//   - WithOpTimeout(2s) sets Config.OpTimeout to 2 seconds
func TestCacheFactory_WithOpTimeout(t *testing.T) {
	t.Parallel()

	var cfg cache.Config
	cache.WithOpTimeout(2 * time.Second)(&cfg)
	assert.Equal(t, 2*time.Second, cfg.OpTimeout)
}

// ---------------------------------------------------------------------------
// Cache Factory - Unknown kind
// ---------------------------------------------------------------------------

// TestCacheFactory_UnknownKindReturnsError tests that New returns an error for
// unknown Kind values.
//
// Why this test is important:
//   - The factory must reject invalid Kind values at construction time to
//     prevent nil-pointer panics in the cache layer
//
// What it tests:
//   - New with an unknown Kind returns a non-nil error
func TestCacheFactory_UnknownKindReturnsError(t *testing.T) {
	t.Parallel()

	_, err := cache.New(cache.Kind(99))
	require.Error(t, err, "expected error for unknown kind, got nil")
}
