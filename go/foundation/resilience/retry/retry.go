// Package retry provides a builder for Retrier implementations with multiple
// backends.
//
// Use New() or NewFromConfig() to create a retrier instance. The builder
// pattern allows selecting between backoff strategies at runtime.
//
//	r, err := retry.New(retry.KindExponential,
//	    retry.WithMaxRetries(5),
//	    retry.WithInitialInterval(200*time.Millisecond),
//	)
//	err = r.Retry(ctx, func() error { return callService() })
package retry

import (
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry/exponential"
)

// Kind specifies which retrier implementation to use.
type Kind int

const (
	// KindExponential uses exponential backoff with jitter (cenkalti/backoff/v5).
	// Suitable for retrying transient network and service failures.
	KindExponential Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindExponential:
		return "exponential"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config is the superset configuration for all retrier kinds.
// Kind-incompatible fields are silently ignored.
type Config struct {
	// Kind specifies which retrier implementation to use.
	Kind Kind

	// MaxRetries is the maximum number of retry attempts before giving up.
	MaxRetries int `yaml:"max_retries" mapstructure:"max_retries"`

	// InitialInterval is the first backoff delay between retries.
	InitialInterval time.Duration `yaml:"initial_interval" mapstructure:"initial_interval"`

	// MaxInterval caps the backoff delay regardless of the multiplier.
	MaxInterval time.Duration `yaml:"max_interval" mapstructure:"max_interval"`

	// Multiplier scales the backoff interval after each retry attempt.
	Multiplier float64 `yaml:"multiplier" mapstructure:"multiplier"`

	// MaxElapsedTime is the absolute deadline for all retries combined.
	MaxElapsedTime time.Duration `yaml:"max_elapsed_time" mapstructure:"max_elapsed_time"`
}

// DefaultConfig returns the default retry configuration with KindExponential.
func DefaultConfig() Config {
	return Config{
		Kind:            KindExponential,
		MaxRetries:      3,
		InitialInterval: 100 * time.Millisecond,
		MaxInterval:     5 * time.Second,
		Multiplier:      2.0,
		MaxElapsedTime:  30 * time.Second,
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(n int) options.Option[Config] {
	return func(c *Config) { c.MaxRetries = n }
}

// WithInitialInterval sets the first backoff delay.
func WithInitialInterval(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.InitialInterval = d }
}

// WithMaxInterval caps the backoff delay.
func WithMaxInterval(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.MaxInterval = d }
}

// WithMultiplier sets the backoff multiplier.
func WithMultiplier(m float64) options.Option[Config] {
	return func(c *Config) { c.Multiplier = m }
}

// WithMaxElapsedTime sets the absolute deadline for all retries.
func WithMaxElapsedTime(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.MaxElapsedTime = d }
}

// New creates a Retrier of the specified kind with optional functional options.
// Returns an error if the kind is unknown.
func New(kind Kind, opts ...options.Option[Config]) (interfaces.Retrier, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewFromConfig creates a Retrier from a Config struct.
// Returns an error if the kind is unknown.
func NewFromConfig(cfg Config) (interfaces.Retrier, error) {
	switch cfg.Kind {
	case KindExponential:
		expCfg := exponential.Config{
			MaxRetries:      cfg.MaxRetries,
			InitialInterval: cfg.InitialInterval,
			MaxInterval:     cfg.MaxInterval,
			Multiplier:      cfg.Multiplier,
			MaxElapsedTime:  cfg.MaxElapsedTime,
		}
		return exponential.New(expCfg), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown retrier kind: %v", cfg.Kind),
		)
	}
}
