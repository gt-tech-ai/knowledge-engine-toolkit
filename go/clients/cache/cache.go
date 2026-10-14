// Package cache builds a ByteCache from config: the always-miss stub (the default) or
// Redis. The concrete backends live in the stub/ and redis/ subpackages.
package cache

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub misses every Get and drops every write (the default).
	KindStub = client.KindStub

	// KindRedis caches in Redis.
	KindRedis client.Kind = "redis"
)

// Config selects and tunes the cache.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("cache").
	Name string `yaml:"name" mapstructure:"name"`

	// Redis configures KindRedis.
	Redis RedisConfig `yaml:"redis" mapstructure:"redis"`

	// DefaultTTL applies when a caller passes a zero TTL.
	DefaultTTL time.Duration `yaml:"default_ttl" mapstructure:"default_ttl"`

	// OpTimeout bounds each cache command; zero inherits the caller's deadline.
	OpTimeout time.Duration `yaml:"op_timeout" mapstructure:"op_timeout"`
}

// RedisConfig configures the Redis backend.
type RedisConfig struct {
	// Addr is the Redis host:port.
	Addr string `yaml:"addr" mapstructure:"addr"`

	// Password is the Redis auth token.
	Password string `yaml:"password" mapstructure:"password"`

	// DB is the Redis database number.
	DB int `yaml:"db" mapstructure:"db"`

	// PoolSize is the connection pool size; zero means 10 * GOMAXPROCS.
	PoolSize int `yaml:"pool_size" mapstructure:"pool_size"`

	// MinIdleConns keeps this many idle connections warm.
	MinIdleConns int `yaml:"min_idle_conns" mapstructure:"min_idle_conns"`

	// FailureMode picks how a Redis error is absorbed: 0 bypasses silently, 1 logs.
	FailureMode int `yaml:"failure_mode" mapstructure:"failure_mode"`

	// TLS encrypts the connection (required by managed Redis with an auth token).
	TLS bool `yaml:"tls" mapstructure:"tls"`
}

// Deps carries the cache's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations
}

// DefaultConfig returns the stub kind, a 5-minute default TTL and a 500ms op timeout.
func DefaultConfig() Config {
	return Config{DefaultTTL: 5 * time.Minute, OpTimeout: 500 * time.Millisecond}
}

// kinds is the cache's backend table.
func kinds() client.Kinds[Config, Deps, interfaces.ByteCache] {
	return client.Kinds[Config, Deps, interfaces.ByteCache]{
		Family:   "cache",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.ByteCache]{
			KindStub: func(context.Context, Config, Deps) (interfaces.ByteCache, error) {
				return stub.New(), nil
			},
			KindRedis: func(
				_ context.Context, cfg Config, deps Deps,
			) (interfaces.ByteCache, error) {
				return redis.New(&redis.Config{
					Addr:         cfg.Redis.Addr,
					Password:     cfg.Redis.Password,
					DB:           cfg.Redis.DB,
					PoolSize:     cfg.Redis.PoolSize,
					MinIdleConns: cfg.Redis.MinIdleConns,
					DefaultTTL:   cfg.DefaultTTL,
					OpTimeout:    cfg.OpTimeout,
					FailureMode:  redis.FailureMode(cfg.Redis.FailureMode),
					TLS:          cfg.Redis.TLS,
					Logger:       deps.Observability().Logger,
				}), nil
			},
		},
	}
}

// NewFromConfig builds the cache cfg.Kind selects and runs it through the client tier,
// without I/O. An unknown kind is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.ByteCache, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
