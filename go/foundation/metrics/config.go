package metrics

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"

// Config is the superset configuration for all metrics kinds.
// Kind-incompatible fields are silently ignored.
type Config struct {
	// Path is the HTTP endpoint path that serves the metrics handler.
	Path string
	// Kind specifies which metrics implementation to use.
	Kind Kind

	// Enabled controls whether metrics collection is active.
	Enabled bool
}

// DefaultConfig returns the default metrics configuration with KindPrometheus.
func DefaultConfig() Config {
	return Config{
		Kind:    KindPrometheus,
		Enabled: true,
		Path:    "/metrics",
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithEnabled sets whether metrics collection is active.
func WithEnabled(enabled bool) options.Option[Config] {
	return func(c *Config) {
		c.Enabled = enabled
	}
}

// WithPath sets the HTTP endpoint path for the metrics handler.
func WithPath(path string) options.Option[Config] {
	return func(c *Config) {
		c.Path = path
	}
}
