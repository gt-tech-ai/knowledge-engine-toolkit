// Package redis provides a Redis-backed cache implementation with singleflight support.
package redis

import (
	"context"
	"crypto/tls"
	"runtime"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertions.
var (
	// Cache satisfies the ByteCache contract (Get/Set/Delete over []byte).
	_ interfaces.ByteCache = (*Cache)(nil)

	// Cache satisfies the CacheInvalidator contract (prefix invalidation).
	_ interfaces.CacheInvalidator = (*Cache)(nil)
)

// escapeGlob escapes the Redis glob metacharacters so a key prefix is matched
// literally by SCAN's MATCH pattern. Without this, a prefix segment containing
// "*"/"?"/"["/"]"/"\" (e.g. an unusual or hostile org id) would silently widen
// the match and delete unrelated keys. Backslash must be escaped first.
func escapeGlob(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		`*`, `\*`,
		`?`, `\?`,
		`[`, `\[`,
		`]`, `\]`,
	).Replace(s)
}

// scanCount is the modest SCAN batch size used by InvalidatePrefix so a sweep
// over a large keyspace does not block Redis on a single call.
const scanCount = 256

// FailureMode determines how cache errors are handled by the cache adapter. In
// bypass mode, cache errors are swallowed and treated as misses. In error
// mode, cache errors are logged but still absorbed (since ByteCache cannot
// return errors).
type FailureMode int

const (
	// FailureModeBypass silently absorbs cache errors (treats as miss).
	FailureModeBypass FailureMode = iota

	// FailureModeError logs cache errors but still absorbs them (ByteCache
	// contract).
	FailureModeError
)

// Config holds Redis connection and behavior settings. Pool size defaults to
// 10 * GOMAXPROCS for throughput. DefaultTTL applies when callers pass zero to
// Set.
type Config struct {
	// Logger is the structured logger for error logging when FailureMode is
	// Error. If nil, error logging is silently skipped regardless of
	// FailureMode.
	Logger interfaces.Logger

	// Addr is the Redis server address in host:port format.
	Addr string

	// Password is the Redis authentication password. Empty means no auth.
	Password string

	// DB is the Redis database number to select after connecting.
	DB int

	// PoolSize is the maximum number of connections in the pool.
	PoolSize int

	// MinIdleConns is the minimum number of idle connections kept open.
	MinIdleConns int

	// DefaultTTL is the expiration applied when callers pass zero to Set.
	DefaultTTL time.Duration

	// OpTimeout is the maximum duration for a single Redis operation (Get,
	// Set, Delete). When non-zero, each operation gets its own context
	// deadline so a slow or unreachable Redis doesn't consume the caller's
	// entire timeout budget. Zero means no per-operation timeout (inherit
	// caller's context).
	OpTimeout time.Duration

	// FailureMode controls whether cache errors are logged (Error) or silently
	// absorbed (Bypass).
	FailureMode FailureMode

	// TLS enables an encrypted (rediss://) connection. Managed Redis such as
	// AWS ElastiCache with an auth token requires in-transit encryption, so a
	// plaintext client would hang and i/o-timeout. Its certificate is issued
	// by a public CA, so standard verification (ServerName from Addr) applies;
	// no custom root or InsecureSkipVerify is needed. Off for local dev Redis.
	TLS bool
}

// DefaultConfig returns sensible defaults for Redis configuration.
func DefaultConfig() Config {
	return Config{
		Addr:         "localhost:6379",
		PoolSize:     10 * runtime.GOMAXPROCS(0),
		MinIdleConns: 5,
		DefaultTTL:   5 * time.Minute,
		OpTimeout:    500 * time.Millisecond,
		FailureMode:  FailureModeBypass,
	}
}

// Cache wraps go-redis with singleflight for cache stampede prevention.
// Implements interfaces.ByteCache by absorbing all errors internally per
// FailureMode. Extended methods (GetOrLoad, Ping, Client) are available on the
// concrete type.
type Cache struct {
	// group deduplicates concurrent cache-miss loads for the same key.
	group singleflight.Group

	// logger for error logging when FailureMode is Error. Nil means skip
	// logging.
	logger interfaces.Logger

	// client is the underlying go-redis client for all Redis operations.
	client *redis.Client

	// defaultTTL is the expiration applied when callers pass zero to Set.
	defaultTTL time.Duration

	// opTimeout is the per-operation context deadline for Redis commands.
	// Zero means inherit the caller's context deadline.
	opTimeout time.Duration

	// failureMode controls whether cache errors are logged or silently absorbed.
	failureMode FailureMode
}

// New creates a Redis-backed cache with the given configuration. Errors from
// Redis operations are absorbed internally per FailureMode.
func New(cfg *Config) *Cache {
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = 5 * time.Minute
	}

	opts := &redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
	}
	if cfg.TLS {
		// The TLS dialer fills ServerName from Addr's host, and ElastiCache's
		// certificate chains to a public CA, so a zero-value config verifies
		// correctly without a custom root or InsecureSkipVerify.
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(opts)

	return &Cache{
		client:      client,
		defaultTTL:  cfg.DefaultTTL,
		opTimeout:   cfg.OpTimeout,
		failureMode: cfg.FailureMode,
		logger:      cfg.Logger,
	}
}

// opCtx returns a context with the per-operation timeout applied. If opTimeout
// is zero, the parent context is returned unchanged. The caller must call the
// returned cancel function when the operation completes.
func (c *Cache) opCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if c.opTimeout > 0 {
		return context.WithTimeout(parent, c.opTimeout)
	}
	return parent, func() {}
}

// Get retrieves a value from Redis. Returns (value, true) on hit, (nil, false)
// on miss or error. Errors are absorbed per FailureMode (logged if Error mode,
// silent if Bypass mode).
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool) {
	ctx, cancel := c.opCtx(ctx)
	defer cancel()

	val, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if apperr.StdIs(err, redis.Nil) {
			// Cache miss (key doesn't exist) - not an error
			return nil, false
		}
		// Connection error or other Redis error - absorb it
		if c.failureMode == FailureModeError && c.logger != nil {
			c.logger.Error("redis cache get failed", "key", key, "error", err)
		}
		return nil, false
	}
	return val, true
}

// Set stores a value in Redis with the given TTL. Errors are absorbed per
// FailureMode (logged if Error mode, silent if Bypass mode).
func (c *Cache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) {
	ctx, cancel := c.opCtx(ctx)
	defer cancel()

	if ttl == 0 {
		ttl = c.defaultTTL
	}
	err := c.client.Set(ctx, key, value, ttl).Err()
	if err != nil && c.failureMode == FailureModeError && c.logger != nil {
		c.logger.Error("redis cache set failed", "key", key, "error", err)
	}
}

// Delete removes a key from Redis. Errors are absorbed per FailureMode (logged
// if Error mode, silent if Bypass mode).
func (c *Cache) Delete(ctx context.Context, key string) {
	ctx, cancel := c.opCtx(ctx)
	defer cancel()

	err := c.client.Del(ctx, key).Err()
	if err != nil && c.failureMode == FailureModeError && c.logger != nil {
		c.logger.Error("redis cache delete failed", "key", key, "error", err)
	}
}

// InvalidatePrefix removes every key whose name begins with prefix, using a
// non-blocking SCAN + UNLINK sweep (UNLINK reclaims memory asynchronously).
// Glob metacharacters in prefix are escaped so the match stays literal. An
// empty or whitespace-only prefix is a guarded no-op — it never flushes the
// cache. Unlike the ByteCache methods, this returns its error (the
// CacheInvalidator contract) so a caller can retry; the error is tagged
// Unavailable so a consumer can treat it as transient.
func (c *Cache) InvalidatePrefix(ctx context.Context, prefix string) error {
	if strings.TrimSpace(prefix) == "" {
		return nil
	}

	pattern := escapeGlob(prefix) + "*"
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, scanCount).Result()
		if err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeUnavailable,
				"redis scan for prefix invalidation",
			)
		}
		if len(keys) > 0 {
			if err := c.client.Unlink(ctx, keys...).Err(); err != nil {
				return apperr.Wrap(
					err,
					apperr.CodeUnavailable,
					"redis unlink for prefix invalidation",
				)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// InvalidateAll clears the cache's Redis database (FLUSHDB). It completes the
// CacheInvalidator contract; the user-context invalidation feature never calls
// it (it would clear unrelated keys on a shared Redis).
func (c *Cache) InvalidateAll(ctx context.Context) error {
	if err := c.client.FlushDB(ctx).Err(); err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"redis flushdb for full invalidation",
		)
	}
	return nil
}

// GetOrLoad retrieves from cache, or on miss, calls loader exactly once
// (singleflight). This is an extended method available only on the concrete
// *Cache type, not through ByteCache. Returns the loaded value and any error
// from the loader function.
func (c *Cache) GetOrLoad(
	ctx context.Context,
	key string,
	loader func() ([]byte, error),
	ttl time.Duration,
) ([]byte, error) {
	// Check cache first
	val, hit := c.Get(ctx, key)
	if hit {
		return val, nil
	}

	// Singleflight: only one goroutine calls loader for a given key
	v, err, _ := c.group.Do(key, func() (any, error) {
		data, err := loader()
		if err != nil {
			return nil, err
		}
		// Best-effort cache population — errors are swallowed because the
		// caller asked for data, not cache success.
		c.Set(ctx, key, data, ttl)
		return data, nil
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]byte), nil
}

// Ping checks the Redis connection. This is an extended method available only
// on the concrete *Cache type.
func (c *Cache) Ping(ctx context.Context) error {
	return apperr.Wrap(c.client.Ping(ctx).Err(), apperr.CodeUnavailable, "redis ping")
}

// Close closes the Redis client connection. Implements io.Closer for resource
// cleanup.
func (c *Cache) Close() error {
	return apperr.Wrap(c.client.Close(), apperr.CodeInternal, "close redis client")
}

// Client returns the underlying Redis client for metrics/health checks. This
// is an extended method available only on the concrete *Cache type.
func (c *Cache) Client() *redis.Client {
	return c.client
}
