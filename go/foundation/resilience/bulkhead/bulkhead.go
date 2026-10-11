// Package bulkhead provides a builder for Bulkhead implementations with
// multiple backends.
//
// Use New() or NewFromConfig() to create a bulkhead instance. The builder
// pattern allows selecting between concurrency limiting strategies at runtime.
//
//	bh, err := bulkhead.New(bulkhead.KindChannel,
//	    bulkhead.WithMaxConcurrent(20),
//	)
//	err = bh.Execute(ctx, func() error { return callDownstream() })
package bulkhead

import (
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/adaptive"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead/channel"
)

// ErrBulkheadFull is re-exported from the channel implementation for convenience.
var ErrBulkheadFull = channel.ErrBulkheadFull

// Kind specifies which bulkhead implementation to use.
type Kind int

const (
	// KindChannel uses a buffered channel as a counting semaphore.
	// Suitable for limiting concurrent goroutines per dependency.
	KindChannel Kind = iota

	// KindAdaptive uses a self-tuning (AIMD) limit that shrinks under latency and recovers.
	// Suitable for a backend whose safe concurrency is unknown or varies.
	KindAdaptive
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindChannel:
		return "channel"
	case KindAdaptive:
		return "adaptive"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config is the superset configuration for all bulkhead kinds.
// Kind-incompatible fields are silently ignored.
type Config struct {
	// Kind specifies which bulkhead implementation to use.
	Kind Kind

	// MaxConcurrent is the maximum number of concurrent operations allowed. For
	// KindAdaptive it is the ceiling the self-tuning limit never grows above.
	MaxConcurrent int `yaml:"max_concurrent" mapstructure:"max_concurrent"`

	// MinConcurrent is the floor the adaptive limit never drops below (KindAdaptive only).
	MinConcurrent int `yaml:"min_concurrent" mapstructure:"min_concurrent"`

	// InitialConcurrent is the adaptive limit's starting value (KindAdaptive only).
	InitialConcurrent int `yaml:"initial_concurrent" mapstructure:"initial_concurrent"`

	// RTTThreshold marks the slow tail for the adaptive limiter: a sample slower than this
	// shrinks the limit (KindAdaptive only).
	RTTThreshold time.Duration `yaml:"rtt_threshold" mapstructure:"rtt_threshold"`

	// BackoffRatio in (0,1) is the adaptive limiter's multiplicative-decrease factor on a
	// slow/failed sample (KindAdaptive only).
	BackoffRatio float64 `yaml:"backoff_ratio" mapstructure:"backoff_ratio"`
}

// DefaultConfig returns the default bulkhead configuration with KindChannel. The adaptive
// fields carry sensible defaults too, so New(KindAdaptive) yields a usable self-tuning
// limiter.
func DefaultConfig() Config {
	return Config{
		Kind:              KindChannel,
		MaxConcurrent:     10,
		MinConcurrent:     1,
		InitialConcurrent: 5,
		RTTThreshold:      100 * time.Millisecond,
		BackoffRatio:      0.9,
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithMaxConcurrent sets the maximum number of concurrent operations. The adaptive kind's
// other tunables (min/initial concurrency, RTT threshold, backoff ratio) are set via a
// Config passed to NewFromConfig — the path the config loader uses — so they need no
// dedicated option helpers.
func WithMaxConcurrent(n int) options.Option[Config] {
	return func(c *Config) { c.MaxConcurrent = n }
}

// New creates a Bulkhead of the specified kind with optional functional options.
// Returns an error if the kind is unknown.
func New(kind Kind, opts ...options.Option[Config]) (interfaces.Bulkhead, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewFromConfig creates a Bulkhead from a Config struct.
// Returns an error if the kind is unknown.
func NewFromConfig(cfg Config) (interfaces.Bulkhead, error) {
	switch cfg.Kind {
	case KindChannel:
		chCfg := channel.Config{
			MaxConcurrent: cfg.MaxConcurrent,
		}
		return channel.New(chCfg), nil

	case KindAdaptive:
		adCfg := adaptive.Config{
			MinConcurrent:     cfg.MinConcurrent,
			MaxConcurrent:     cfg.MaxConcurrent,
			InitialConcurrent: cfg.InitialConcurrent,
			RTTThreshold:      cfg.RTTThreshold,
			BackoffRatio:      cfg.BackoffRatio,
		}
		return adaptive.New(adCfg), nil

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown bulkhead kind: %v", cfg.Kind),
		)
	}
}
