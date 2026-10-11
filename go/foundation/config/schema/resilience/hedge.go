package resilience

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// HedgeConfig tunes the tail-latency hedger (hedge.Config), read from the
// `resilience.hedge` overlay. Hedging duplicates load and is only safe for
// IDEMPOTENT / read-only paths, so it is DISABLED by default and opted into per
// path via Kind "delay".
type HedgeConfig struct {
	// Kind selects the hedger: "disabled" (run once, the safe default) or "delay"
	// (fire a backup attempt after Delay and take the first responder). Empty
	// defaults to the primitive's default (disabled); reswire maps this string to
	// the hedge.Kind and fails loudly on an unknown value.
	Kind string `mapstructure:"kind"`

	// Delay is how long to wait for the first attempt before firing the backup
	// (Kind "delay" only).
	Delay time.Duration `mapstructure:"delay"`
}

// DefaultHedgeConfig returns defaults identical to hedge.DefaultConfig() (Kind
// "disabled", 50ms delay) — behavior-preserving: an absent overlay hedges nothing.
func DefaultHedgeConfig() HedgeConfig {
	return HedgeConfig{
		Kind:  "disabled",
		Delay: 50 * time.Millisecond,
	}
}

// Validate rejects a negative delay. The kind string is validated (and mapped to a
// hedge.Kind) at build time by reswire, which fails loudly on an unknown value.
func (c HedgeConfig) Validate() error {
	if c.Delay < 0 {
		return apperr.InvalidInput("resilience.hedge.delay must be >= 0")
	}
	return nil
}
