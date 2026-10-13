// Package decoration is the config surface that picks which tiers below the request
// workflow carry the logging, metrics and tracing decorators; decorate.NewTiers turns it
// into the per-tier observability bundles. The workflow tier is always observed, so each
// request is logged, metered and traced once; the lower tiers are diagnostics that show
// where a request spends its time. Resilience (timeouts, retry, circuit breakers,
// recovery) runs at every tier whatever these say. Default: every lower tier off.
package decoration

import (
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// SectionKey is the config key the decoration section lives under.
const SectionKey = "decoration"

// Config holds the per-tier observability switches.
type Config struct {
	// Pipelines observes each pipeline stage (decoration.pipelines).
	Pipelines bool `mapstructure:"pipelines"`

	// Services observes each service operation (decoration.services).
	Services bool `mapstructure:"services"`

	// Repositories observes each repository operation (decoration.repositories).
	Repositories bool `mapstructure:"repositories"`
}

// DefaultConfig returns the fail-safe default: only the request workflow is observed.
func DefaultConfig() Config {
	return Config{}
}

// Load reads the decoration section from loader over DefaultConfig(): an absent section
// keeps the default, and a section that fails to decode is returned as CodeInvalidInput.
func Load(loader interfaces.ConfigLoader) (Config, error) {
	cfg := DefaultConfig()
	if loader.Get(SectionKey) == nil {
		return cfg, nil
	}
	if err := loader.UnmarshalKey(SectionKey, &cfg); err != nil {
		return Config{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"load decoration config",
		)
	}
	return cfg, nil
}
