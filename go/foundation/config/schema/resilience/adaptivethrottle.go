package resilience

import (
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// AdaptiveThrottleConfig tunes the client-side SRE adaptive throttler
// (adaptivethrottle.Config), read from the `resilience.adaptive_throttle` overlay.
// Load-shedding is opt-in, so Kind defaults to "disabled" (a no-op pass-through);
// "enabled" composes the throttler OUTERMOST of the retry budget.
type AdaptiveThrottleConfig struct {
	// Kind selects load shedding: "disabled" (no-op pass-through, the safe default)
	// or "enabled" (shed a growing fraction of requests when the backend's accept
	// rate drops). Empty defaults to disabled; reswire fails loudly on an unknown value.
	Kind string `mapstructure:"kind"`

	// K is the accepts multiplier in the SRE rejection formula; higher K sheds less
	// aggressively (Kind "enabled" only).
	K float64 `mapstructure:"k"`

	// Decay is the per-attempt exponential decay (0 < Decay <= 1) applied to the
	// request/accept windows (Kind "enabled" only).
	Decay float64 `mapstructure:"decay"`
}

// DefaultAdaptiveThrottleConfig returns the behavior-preserving default: disabled
// (no shedding), with the primitive's SRE-typical K/Decay ready for when it is enabled.
func DefaultAdaptiveThrottleConfig() AdaptiveThrottleConfig {
	return AdaptiveThrottleConfig{
		Kind:  "disabled",
		K:     2.0,
		Decay: 0.98,
	}
}

// Validate rejects a negative K and an out-of-[0,1] Decay. The kind string is validated
// (and mapped) at build time by reswire, which fails loudly on an unknown value.
func (c AdaptiveThrottleConfig) Validate() error {
	if c.K < 0 {
		return apperr.InvalidInput("resilience.adaptive_throttle.k must be >= 0")
	}
	if c.Decay < 0 || c.Decay > 1 {
		return apperr.InvalidInput("resilience.adaptive_throttle.decay must be in [0,1]")
	}
	return nil
}
