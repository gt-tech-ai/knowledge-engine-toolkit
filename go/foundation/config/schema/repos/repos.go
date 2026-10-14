// Package repos is the config surface for the repository (data-access) layer: the
// entity cache's TTL, version and key prefix. The repository's decoration (timeout,
// retry, breaker, observability) is the decoration config's repositories tier, not
// duplicated here. Pure data.
package repos

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// CachingConfig tunes the repository caching decorator.
type CachingConfig struct {
	// KeyPrefix namespaces cache keys (empty = no prefix, the default).
	KeyPrefix string `mapstructure:"key_prefix"`

	// TTL is how long a cached entity remains valid (default 5m).
	TTL time.Duration `mapstructure:"ttl"`

	// Version namespaces cache keys so a schema change invalidates them (default 1).
	Version int `mapstructure:"version"`
}

// Config tunes the repository's entity cache. Its decoration layers (timeout, retry,
// breaker, observability) are the decoration config's repositories tier.
type Config struct {
	// Caching tunes the repository cache decorator.
	Caching CachingConfig `mapstructure:"caching"`
}

// DefaultConfig returns the repository defaults: a 5m cache TTL and cache version 1.
func DefaultConfig() Config {
	return Config{Caching: CachingConfig{TTL: 5 * time.Minute, Version: 1}}
}

// Validate rejects a negative cache TTL or version.
func (c Config) Validate() error {
	if c.Caching.TTL < 0 {
		return apperr.InvalidInput("repos.caching.ttl must be >= 0")
	}
	if c.Caching.Version < 0 {
		return apperr.InvalidInput("repos.caching.version must be >= 0")
	}
	return nil
}
