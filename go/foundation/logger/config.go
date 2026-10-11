package logger

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"

// Config is the superset configuration for all logger kinds. Kind-incompatible fields are
// silently ignored (e.g., Format and RedactPII are ignored when Kind=KindStdlib).
type Config struct {
	// Level sets the minimum log severity (debug, info, warn, error).
	// Applies to all logger kinds.
	Level string

	// Format selects the output encoding: "json" or "text".
	// Only used when Kind is KindZap, ignored for KindStdlib.
	Format string

	// Kind specifies which logger implementation to use.
	Kind Kind

	// RedactPII enables automatic PII scrubbing.
	// Only used when Kind is KindZap, ignored for KindStdlib.
	RedactPII bool

	// AddSource enables source code location in log records.
	// Only used when Kind is KindStdlib, ignored for KindZap.
	AddSource bool
}

// DefaultConfig returns the default logger configuration with KindZap and sensible
// defaults.
func DefaultConfig() Config {
	return Config{
		Kind:      KindZap,
		Level:     "info",
		Format:    "json",
		RedactPII: true,
		AddSource: true,
	}
}

// ToOptions converts this Config to a slice of Option functions.
// Useful for converting configs to builder options.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithLevel sets the log level.
func WithLevel(level string) options.Option[Config] {
	return func(c *Config) {
		c.Level = level
	}
}

// WithFormat sets the log format (json or text).
// Only used when Kind is KindZap.
func WithFormat(format string) options.Option[Config] {
	return func(c *Config) {
		c.Format = format
	}
}

// WithRedactPII enables or disables PII redaction.
// Only used when Kind is KindZap.
func WithRedactPII(enabled bool) options.Option[Config] {
	return func(c *Config) {
		c.RedactPII = enabled
	}
}

// WithAddSource enables or disables source code location in log records.
// Only used when Kind is KindStdlib.
func WithAddSource(enabled bool) options.Option[Config] {
	return func(c *Config) {
		c.AddSource = enabled
	}
}
