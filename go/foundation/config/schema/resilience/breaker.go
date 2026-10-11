package resilience

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// BreakerConfig tunes the gobreaker circuit-breaker decorator
// (circuitbreaker.Config). Name and Kind stay with the provider (identity /
// impl selection); this is the tuning surface only.
type BreakerConfig struct {
	// Interval is the cyclic period of the closed state for clearing counts.
	Interval time.Duration `mapstructure:"interval"`

	// Timeout is how long the breaker stays open before transitioning to
	// half-open.
	Timeout time.Duration `mapstructure:"timeout"`

	// FailureRatio trips the breaker when the failure ratio over the interval
	// reaches this value (0..1), once MinRequests is met.
	FailureRatio float64 `mapstructure:"failure_ratio"`

	// MaxRequests is the number of requests allowed to pass in the half-open
	// state before the breaker closes.
	MaxRequests uint32 `mapstructure:"max_requests"`

	// ConsecutiveFailures trips the breaker after this many consecutive failures.
	ConsecutiveFailures uint32 `mapstructure:"consecutive_failures"`

	// MinRequests is the minimum request count in the interval before the
	// failure-ratio trip applies.
	MinRequests uint32 `mapstructure:"min_requests"`
}

// DefaultBreakerConfig returns defaults identical to
// circuitbreaker.DefaultConfig(name) (excluding the per-provider Name).
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		MaxRequests:         1,
		Interval:            60 * time.Second,
		Timeout:             30 * time.Second,
		ConsecutiveFailures: 5,
		FailureRatio:        0.5,
		MinRequests:         10,
	}
}

// Validate rejects out-of-range breaker tuning.
func (c BreakerConfig) Validate() error {
	if c.FailureRatio < 0 || c.FailureRatio > 1 {
		return apperr.InvalidInput(
			"resilience.circuit_breaker.failure_ratio must be in [0,1]",
		)
	}
	if c.Interval < 0 {
		return apperr.InvalidInput("resilience.circuit_breaker.interval must be >= 0")
	}
	if c.Timeout < 0 {
		return apperr.InvalidInput("resilience.circuit_breaker.timeout must be >= 0")
	}
	return nil
}
