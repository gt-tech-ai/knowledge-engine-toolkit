// Package workflows is the config surface for the workflow (orchestration) layer:
// the per-workflow operation timeout applied through workflow.Builder.WithTimeout.
// Default is no timeout. Load reads the section from a ConfigLoader.
package workflows

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// SectionKey is the config key the workflow section lives under.
const SectionKey = "workflows"

// Config tunes the workflow layer.
type Config struct {
	// Timeout is the per-workflow operation timeout (0 = no timeout, the default).
	Timeout time.Duration `mapstructure:"timeout"`
}

// DefaultConfig returns the workflow default: no per-workflow timeout.
func DefaultConfig() Config {
	return Config{}
}

// Validate rejects a negative timeout.
func (c Config) Validate() error {
	if c.Timeout < 0 {
		return apperr.InvalidInput("workflows.timeout must be >= 0")
	}
	return nil
}

// Load reads the workflows section from loader over DefaultConfig(): an absent
// section keeps the default, and a section that fails to decode or validate is
// returned as CodeInvalidInput.
func Load(loader interfaces.ConfigLoader) (Config, error) {
	cfg := DefaultConfig()
	if loader.Get(SectionKey) != nil {
		if err := loader.UnmarshalKey(SectionKey, &cfg); err != nil {
			return Config{}, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load workflows config",
			)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
