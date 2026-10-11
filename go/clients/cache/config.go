package cache

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Config is the configuration for the Redis cache backend.
type Config struct {
	// Logger is the structured logger passed to the underlying cache backend.
	// If nil, error logging is silently skipped.
	Logger interfaces.Logger

	// Redis holds Redis-specific configuration.
	Redis RedisConfig

	// Kind specifies which cache implementation to use.
	Kind Kind

	// DefaultTTL is the expiration applied when callers pass zero to Set.
	DefaultTTL time.Duration

	// OpTimeout is the per-operation context deadline for cache commands.
	// Zero means no per-operation timeout (inherit caller's context).
	OpTimeout time.Duration
}

// RedisConfig holds Redis-specific configuration. This is a subset of
// redis.Config, adapted for the parent cache package.
type RedisConfig struct {
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

	// FailureMode controls whether cache errors are logged (1=Error) or
	// silently absorbed (0=Bypass). Maps to redis.FailureMode enum:
	// 0=FailureModeBypass, 1=FailureModeError.
	FailureMode int

	// TLS enables an encrypted connection to Redis (required by managed Redis
	// such as ElastiCache with an auth token). Off for local dev.
	TLS bool
}

// DefaultConfig returns the default cache configuration with KindRedis and
// 5-minute TTL.
func DefaultConfig() Config {
	return Config{
		Kind:       KindRedis,
		DefaultTTL: 5 * time.Minute,
		OpTimeout:  500 * time.Millisecond,
	}
}

// ToOptions converts this Config to a slice of Option functions. Useful for
// converting configs to builder options.
func (c *Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = *c },
	}
}

// WithDefaultTTL sets the default TTL for the cache.
func WithDefaultTTL(ttl time.Duration) options.Option[Config] {
	return func(c *Config) {
		c.DefaultTTL = ttl
	}
}

// WithRedisAddr sets the Redis server address.
func WithRedisAddr(addr string) options.Option[Config] {
	return func(c *Config) {
		c.Redis.Addr = addr
	}
}

// WithRedisPassword sets the Redis authentication password.
func WithRedisPassword(password string) options.Option[Config] {
	return func(c *Config) {
		c.Redis.Password = password
	}
}

// WithRedisDB sets the Redis database number.
func WithRedisDB(db int) options.Option[Config] {
	return func(c *Config) {
		c.Redis.DB = db
	}
}

// WithRedisPoolSize sets the Redis connection pool size.
func WithRedisPoolSize(size int) options.Option[Config] {
	return func(c *Config) {
		c.Redis.PoolSize = size
	}
}

// WithRedisFailureMode sets the Redis failure mode.
// 0 = FailureModeBypass (silent), 1 = FailureModeError (logged).
func WithRedisFailureMode(mode int) options.Option[Config] {
	return func(c *Config) {
		c.Redis.FailureMode = mode
	}
}

// WithRedisTLS enables (or disables) an encrypted connection to Redis. Required
// by managed Redis such as ElastiCache with an auth token.
func WithRedisTLS(enabled bool) options.Option[Config] {
	return func(c *Config) {
		c.Redis.TLS = enabled
	}
}

// WithOpTimeout sets the per-operation context deadline for cache commands.
// Zero disables per-operation timeouts (inherits caller's context).
func WithOpTimeout(d time.Duration) options.Option[Config] {
	return func(c *Config) {
		c.OpTimeout = d
	}
}
