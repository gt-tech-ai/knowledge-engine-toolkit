// Package cache provides a builder for ByteCache implementations with multiple
// backends.
//
// Use New() or NewFromConfig() to create a cache instance. The builder pattern
// allows selecting between Redis-backed (or future) implementations at runtime
// based on configuration.
//
// Example:
//
//	// Create a Redis cache with custom TTL
//	c, err := cache.New(cache.KindRedis, cache.WithDefaultTTL(10*time.Minute))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer c.(io.Closer).Close()
//
//	// Create from config
//	cfg := cache.DefaultConfig()
//	cfg.Kind = cache.KindRedis
//	c, err := cache.NewFromConfig(cfg)
package cache

import (
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/redis"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which cache implementation to use.
type Kind int

const (
	// KindRedis uses a Redis-backed cache with singleflight support. Suitable
	// for distributed deployments with shared cache.
	KindRedis Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindRedis:
		return "redis"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// New creates a ByteCache of the specified kind with optional functional
// options. Returns an error if the kind is unknown.
//
// The returned ByteCache may implement io.Closer. Call type assertion and
// Close() to clean up resources.
//
// Example:
//
//	c, err := cache.New(cache.KindRedis, cache.WithDefaultTTL(10*time.Minute))
//	if err != nil {
//	    return err
//	}
//	defer c.(io.Closer).Close()
func New(kind Kind, opts ...options.Option[Config]) (interfaces.ByteCache, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(&cfg)
}

// NewFromConfig creates a ByteCache from a Config struct. Returns an error if
// the kind is unknown.
func NewFromConfig(cfg *Config) (interfaces.ByteCache, error) {
	switch cfg.Kind {
	case KindRedis:
		redisCfg := redis.Config{
			Addr:         cfg.Redis.Addr,
			Password:     cfg.Redis.Password,
			DB:           cfg.Redis.DB,
			PoolSize:     cfg.Redis.PoolSize,
			MinIdleConns: cfg.Redis.MinIdleConns,
			DefaultTTL:   cfg.DefaultTTL,
			OpTimeout:    cfg.OpTimeout,
			FailureMode:  redis.FailureMode(cfg.Redis.FailureMode),
			TLS:          cfg.Redis.TLS,
			Logger:       cfg.Logger,
		}
		return redis.New(&redisCfg), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown cache kind: %v", cfg.Kind),
		)
	}
}
