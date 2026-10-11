// Package circuitbreaker provides a builder for CircuitBreaker implementations
// with multiple backends.
//
// Use New() or NewFromConfig() to create a circuit breaker instance. The builder
// pattern allows selecting between circuit breaker strategies at runtime.
//
//	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "payments",
//	    circuitbreaker.WithConsecutiveFailures(3),
//	    circuitbreaker.WithTimeout(10*time.Second),
//	)
//	err = cb.Execute(func() error { return client.Charge(ctx, amount) })
package circuitbreaker

import (
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	gb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker/gobreaker"
)

// Kind specifies which circuit breaker implementation to use.
type Kind int

const (
	// KindGoBreaker uses sony/gobreaker/v2 for circuit breaking.
	// Suitable for production workloads with state machine semantics.
	KindGoBreaker Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindGoBreaker:
		return "gobreaker"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config is the superset configuration for all circuit breaker kinds.
// Kind-incompatible fields are silently ignored.
type Config struct {
	// Name identifies the circuit breaker instance for logging and metrics.
	Name string `yaml:"name" mapstructure:"name"`

	// Kind specifies which circuit breaker implementation to use.
	Kind Kind

	// Interval is the cyclic period of the closed state for clearing internal counts.
	Interval time.Duration `yaml:"interval" mapstructure:"interval"`

	// Timeout is the duration the circuit stays open before transitioning to half-open.
	Timeout time.Duration `yaml:"timeout" mapstructure:"timeout"`

	// MaxRequests is the number of requests allowed in the half-open state.
	MaxRequests uint32 `yaml:"max_requests" mapstructure:"max_requests"`

	// ConsecutiveFailures is the threshold of consecutive failures that trips the breaker.
	ConsecutiveFailures uint32 `yaml:"consecutive_failures" mapstructure:"consecutive_failures"`

	// FailureRatio trips the breaker when the failure fraction over the interval
	// reaches it (once MinRequests is met), independent of consecutive failures.
	FailureRatio float64 `yaml:"failure_ratio" mapstructure:"failure_ratio"`

	// MinRequests is the minimum requests in the interval before FailureRatio applies.
	MinRequests uint32 `yaml:"min_requests" mapstructure:"min_requests"`
}

// DefaultConfig returns the default circuit breaker configuration with KindGoBreaker.
func DefaultConfig(name string) Config {
	return Config{
		Kind:                KindGoBreaker,
		Name:                name,
		MaxRequests:         1,
		Interval:            60 * time.Second,
		Timeout:             30 * time.Second,
		ConsecutiveFailures: 5,
		FailureRatio:        0.5,
		MinRequests:         10,
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithName sets the circuit breaker name.
func WithName(name string) options.Option[Config] {
	return func(c *Config) { c.Name = name }
}

// WithMaxRequests sets the number of requests allowed in half-open state.
func WithMaxRequests(n uint32) options.Option[Config] {
	return func(c *Config) { c.MaxRequests = n }
}

// WithInterval sets the cyclic period of the closed state.
func WithInterval(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.Interval = d }
}

// WithTimeout sets the duration the circuit stays open.
func WithTimeout(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.Timeout = d }
}

// WithConsecutiveFailures sets the failure threshold that trips the breaker.
func WithConsecutiveFailures(n uint32) options.Option[Config] {
	return func(c *Config) { c.ConsecutiveFailures = n }
}

// WithFailureRatio sets the failure-ratio threshold that trips the breaker once
// the minimum request volume is met.
func WithFailureRatio(r float64) options.Option[Config] {
	return func(c *Config) { c.FailureRatio = r }
}

// WithMinRequests sets the minimum request volume before the failure-ratio guard
// applies.
func WithMinRequests(n uint32) options.Option[Config] {
	return func(c *Config) { c.MinRequests = n }
}

// New creates a CircuitBreaker of the specified kind with optional functional options.
// The name parameter identifies the circuit breaker instance for logging and metrics.
// Returns an error if the kind is unknown.
func New(
	kind Kind,
	name string,
	opts ...options.Option[Config],
) (interfaces.CircuitBreaker, error) {
	cfg := DefaultConfig(name)
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewFromConfig creates a CircuitBreaker from a Config struct.
// Returns an error if the kind is unknown.
func NewFromConfig(cfg Config) (interfaces.CircuitBreaker, error) {
	switch cfg.Kind {
	case KindGoBreaker:
		gbCfg := gb.Config{
			Name:                cfg.Name,
			MaxRequests:         cfg.MaxRequests,
			Interval:            cfg.Interval,
			Timeout:             cfg.Timeout,
			ConsecutiveFailures: cfg.ConsecutiveFailures,
			FailureRatio:        cfg.FailureRatio,
			MinRequests:         cfg.MinRequests,
		}
		return gb.New(gbCfg), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown circuit breaker kind: %v", cfg.Kind),
		)
	}
}
