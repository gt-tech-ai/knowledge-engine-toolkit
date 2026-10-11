// Package ratelimiter provides a builder for RateLimiter implementations with
// multiple backends.
//
// Use New() or NewFromConfig() to create a rate limiter instance. The builder
// pattern allows selecting between rate limiting strategies at runtime.
//
//	lim, err := ratelimiter.New(ratelimiter.KindToken,
//	    ratelimiter.WithRate(50),
//	    ratelimiter.WithBurst(5),
//	)
//	if !lim.Allow() {
//	    return ratelimiter.ErrRateLimited
//	}
package ratelimiter

import (
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/ratelimiter/token"
)

// ErrRateLimited is returned when the rate limiter rejects a request.
var ErrRateLimited = apperr.New(apperr.CodeUnavailable, "rate limited")

// Kind specifies which rate limiter implementation to use.
type Kind int

const (
	// KindToken uses a token bucket algorithm (golang.org/x/time/rate).
	// Suitable for steady-rate throttling with controlled bursts.
	KindToken Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindToken:
		return "token"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config is the superset configuration for all rate limiter kinds.
// Kind-incompatible fields are silently ignored.
type Config struct {
	// Kind specifies which rate limiter implementation to use.
	Kind Kind

	// Rate is the sustained events per second allowed.
	Rate float64 `yaml:"rate" mapstructure:"rate"`

	// Burst is the maximum number of events allowed in a single burst.
	Burst int `yaml:"burst" mapstructure:"burst"`
}

// DefaultConfig returns the default rate limiter configuration with KindToken.
func DefaultConfig() Config {
	return Config{
		Kind:  KindToken,
		Rate:  100,
		Burst: 10,
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithRate sets the sustained events per second.
func WithRate(r float64) options.Option[Config] {
	return func(c *Config) { c.Rate = r }
}

// WithBurst sets the maximum burst size.
func WithBurst(b int) options.Option[Config] {
	return func(c *Config) { c.Burst = b }
}

// New creates a RateLimiter of the specified kind with optional functional
// options. Returns an error if the kind is unknown.
func New(kind Kind, opts ...options.Option[Config]) (interfaces.RateLimiter, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewFromConfig creates a RateLimiter from a Config struct. Returns an error
// if the kind is unknown.
func NewFromConfig(cfg Config) (interfaces.RateLimiter, error) {
	switch cfg.Kind {
	case KindToken:
		tokenCfg := token.Config{
			Rate:  cfg.Rate,
			Burst: cfg.Burst,
		}
		return token.New(tokenCfg), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown rate limiter kind: %v", cfg.Kind),
		)
	}
}
