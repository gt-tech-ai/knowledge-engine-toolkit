package resilience

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// defaultBulkheadConfigMaxConcurrent is the default MaxConcurrent of BulkheadConfig.
const defaultBulkheadConfigMaxConcurrent = 10

// defaultBulkheadConfigInitialConcurrent is the default InitialConcurrent of
// BulkheadConfig.
const defaultBulkheadConfigInitialConcurrent = 5

// defaultBulkheadConfigRTTThreshold is the default RTTThreshold of BulkheadConfig.
const defaultBulkheadConfigRTTThreshold = 100 * time.Millisecond

// defaultBulkheadConfigBackoffRatio is the default BackoffRatio of BulkheadConfig.
const defaultBulkheadConfigBackoffRatio = 0.9

// BulkheadConfig tunes the bulkhead concurrency limiter (bulkhead.Config), read from
// the `resilience.adaptive_limit` overlay. The adaptive fields (Min/Initial,
// RTTThreshold, BackoffRatio) apply to Kind "adaptive"; MaxConcurrent is the ceiling
// for every kind.
type BulkheadConfig struct {
	// Kind selects the bulkhead implementation: "channel" (fixed counting semaphore)
	// or "adaptive" (self-tuning AIMD limit). Empty defaults to the primitive's default
	// (channel); reswire maps this string to the bulkhead.Kind and fails loudly on an
	// unknown value.
	Kind string `mapstructure:"kind"`

	// RTTThreshold marks the slow tail for the adaptive limiter: a sample slower
	// than this shrinks the limit (Kind "adaptive" only).
	RTTThreshold time.Duration `mapstructure:"rtt_threshold"`

	// BackoffRatio in (0,1] is the adaptive limiter's multiplicative-decrease
	// factor on a slow sample (KindAdaptive only).
	BackoffRatio float64 `mapstructure:"backoff_ratio"`

	// MaxConcurrent is the maximum number of concurrent operations allowed (the
	// ceiling the adaptive limit never grows above).
	MaxConcurrent int `mapstructure:"max_concurrent"`

	// MinConcurrent is the floor the adaptive limit never drops below.
	MinConcurrent int `mapstructure:"min_concurrent"`

	// InitialConcurrent is the adaptive limit's starting value.
	InitialConcurrent int `mapstructure:"initial_concurrent"`
}

// DefaultBulkheadConfig returns defaults identical to bulkhead.DefaultConfig()
// (Kind "channel", the primitive default).
func DefaultBulkheadConfig() BulkheadConfig {
	return BulkheadConfig{
		Kind:              "channel",
		MaxConcurrent:     defaultBulkheadConfigMaxConcurrent,
		MinConcurrent:     1,
		InitialConcurrent: defaultBulkheadConfigInitialConcurrent,
		RTTThreshold:      defaultBulkheadConfigRTTThreshold,
		BackoffRatio:      defaultBulkheadConfigBackoffRatio,
	}
}

// Validate rejects out-of-range bulkhead tuning. The kind string is validated (and
// mapped to a bulkhead.Kind) at build time by reswire, which fails loudly on an
// unknown value.
func (c BulkheadConfig) Validate() error {
	if c.MaxConcurrent < 1 {
		return apperr.InvalidInput(
			"resilience.adaptive_limit.max_concurrent must be >= 1",
		)
	}

	if c.MinConcurrent < 0 {
		return apperr.InvalidInput(
			"resilience.adaptive_limit.min_concurrent must be >= 0",
		)
	}

	if c.BackoffRatio <= 0 || c.BackoffRatio > 1 {
		return apperr.InvalidInput(
			"resilience.adaptive_limit.backoff_ratio must be in (0,1]",
		)
	}

	return nil
}
