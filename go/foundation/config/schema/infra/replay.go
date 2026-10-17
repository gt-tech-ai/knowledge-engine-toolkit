package infra

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// ReplayConfig tunes the WebSocket reconnect-replay buffer: the per-connection outbound
// message tail buffered for resend when a client's socket briefly drops. The backend
// (in-process memory vs cross-pod Redis) is selected at the composition root by whether
// the shared Redis client is present — single-pod dev has none (memory); multi-pod
// staging/prod shares the lock/backplane client (redis) — so there is no kind here, only
// the bound + lifetimes.
type ReplayConfig struct {
	// MaxSize bounds the retained messages per connection; the newest MaxSize are kept.
	MaxSize int `mapstructure:"max_size"`

	// TTL is how long a connection's buffer lives after its last message — a short
	// recovery window.
	TTL time.Duration `mapstructure:"ttl"`
}

// DefaultReplayConfig returns a 100-message, 5-minute buffer, matching the base
// config. Dev inherits this (memory backend, no Redis client);
// staging/prod share the Redis client so the same values drive the redis backend.
func DefaultReplayConfig() ReplayConfig {
	return ReplayConfig{
		MaxSize: 100,
		TTL:     5 * time.Minute,
	}
}

// Validate rejects a non-positive bound or TTL.
func (c ReplayConfig) Validate() error {
	if c.MaxSize <= 0 {
		return apperr.InvalidInput("replay.max_size must be positive")
	}
	if c.TTL <= 0 {
		return apperr.InvalidInput("replay.ttl must be positive")
	}
	return nil
}
