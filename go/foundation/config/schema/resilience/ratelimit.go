package resilience

import apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"

// RateLimitConfig tunes the token-bucket rate limiter (ratelimiter/token.Config),
// whose own defaults are Rate 100 / Burst 10.
type RateLimitConfig struct {
	// Rate is the sustained events per second allowed.
	Rate float64 `mapstructure:"rate"`

	// Burst is the maximum number of events allowed in a single burst.
	Burst int `mapstructure:"burst"`
}

// DefaultRateLimitConfig returns defaults identical to token.DefaultConfig().
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		Rate:  100,
		Burst: 10,
	}
}

// Validate rejects out-of-range rate-limit tuning.
func (c RateLimitConfig) Validate() error {
	if c.Rate < 0 {
		return apperr.InvalidInput("resilience.rate_limit.rate must be >= 0")
	}
	if c.Burst < 0 {
		return apperr.InvalidInput("resilience.rate_limit.burst must be >= 0")
	}
	return nil
}
