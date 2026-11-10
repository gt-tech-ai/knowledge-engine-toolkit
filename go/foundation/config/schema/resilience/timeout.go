package resilience

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// defaultLayerDeadline is the default deadline of the timeout layer.
const defaultLayerDeadline = 30 * time.Second

// TimeoutConfig tunes a per-operation timeout wrapper. There is no dedicated
// timeout primitive (it is a context deadline applied by the caller); this
// surface lets a provider read the deadline from config rather than hardcode it.
type TimeoutConfig struct {
	// Timeout is the per-operation deadline (0 = no timeout).
	Timeout time.Duration `mapstructure:"timeout"`
}

// DefaultTimeoutConfig returns a conservative 30s per-operation timeout.
func DefaultTimeoutConfig() TimeoutConfig {
	return TimeoutConfig{Timeout: defaultLayerDeadline}
}

// Validate rejects a negative timeout.
func (c TimeoutConfig) Validate() error {
	if c.Timeout < 0 {
		return apperr.InvalidInput("resilience.timeout.timeout must be >= 0")
	}

	return nil
}
