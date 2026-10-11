package jobs

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Config is the top-level configuration for the jobs package.
type Config struct {
	// River holds River-specific configuration.
	River river.Config

	// Kind specifies which job queue implementation to use.
	Kind Kind
}

// DefaultConfig returns the default jobs configuration with KindRiver.
func DefaultConfig() Config {
	return Config{
		Kind: KindRiver,
	}
}

// WithDatabaseURL sets the PostgreSQL database URL for the River backend.
func WithDatabaseURL(url string) options.Option[Config] {
	return func(c *Config) {
		c.River.DatabaseURL = url
	}
}
