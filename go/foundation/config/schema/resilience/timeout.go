package resilience

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TimeoutConfig tunes a per-operation timeout wrapper. There is no dedicated
// timeout primitive (it is a context deadline applied by the caller); this
// surface lets a provider read the deadline from config rather than hardcode it.
type TimeoutConfig struct {
	// Timeout is the per-operation deadline (0 = no timeout).
	Timeout time.Duration `mapstructure:"timeout"`
}

// DefaultTimeoutConfig returns a conservative 30s per-operation timeout.
func DefaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{Timeout: 30 * time.Second}
}

// Validate rejects a negative timeout.
func (c TimeoutConfig) Validate() error {
	if c.Timeout < 0 {
		return apperr.InvalidInput("resilience.timeout.timeout must be >= 0")
	}
	return nil
}
