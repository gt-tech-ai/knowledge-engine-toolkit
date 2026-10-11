// Package resilience is the tunable config surface for the foundation resilience
// decorators (retry, breaker, rate-limit, bulkhead, timeout, budget) — the
// per-environment knobs threaded through the repo/client/interceptor providers
// Each struct mirrors its primitive's tunable fields
// with defaults identical to the primitive's DefaultConfig(), so adopting the
// config surface is behavior-preserving; the providers convert these into the
// primitive's options. Kept a pure-data config tier (no foundation/resilience
// import) mirroring the infra/S3Config convention; the defaults tests guard drift.
package resilience

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// RetryConfig tunes the exponential-backoff retry decorator (retry.Config).
type RetryConfig struct {
	// InitialInterval is the first backoff delay between retries.
	InitialInterval time.Duration `mapstructure:"initial_interval"`

	// MaxInterval caps the backoff delay regardless of the multiplier.
	MaxInterval time.Duration `mapstructure:"max_interval"`

	// MaxElapsedTime bounds the total time spent retrying (0 = no bound).
	MaxElapsedTime time.Duration `mapstructure:"max_elapsed_time"`

	// Multiplier scales the backoff interval after each attempt.
	Multiplier float64 `mapstructure:"multiplier"`

	// MaxRetries is the maximum number of retry attempts before giving up.
	MaxRetries int `mapstructure:"max_retries"`
}

// DefaultRetryConfig returns defaults identical to retry.DefaultConfig(), so the
// config surface with no overlay matches the retry package's own defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:      3,
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     5 * time.Second,
		Multiplier:      2.0,
		MaxElapsedTime:  30 * time.Second,
	}
}

// Validate rejects out-of-range retry tuning.
func (c RetryConfig) Validate() error {
	if c.MaxRetries < 0 {
		return apperr.InvalidInput("resilience.retry.max_retries must be >= 0")
	}
	if c.InitialInterval < 0 {
		return apperr.InvalidInput("resilience.retry.initial_interval must be >= 0")
	}
	if c.MaxInterval < 0 {
		return apperr.InvalidInput("resilience.retry.max_interval must be >= 0")
	}
	if c.Multiplier < 1 {
		return apperr.InvalidInput("resilience.retry.multiplier must be >= 1")
	}
	return nil
}
