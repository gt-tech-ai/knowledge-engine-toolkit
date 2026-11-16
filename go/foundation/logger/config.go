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

	// Schema selects the field set every line carries: SchemaV1 (the default; ""
	// means v1) or SchemaV2, the unified set (ARCHITECTURE.md#logging). Applies to
	// all logger kinds; an unknown schema fails NewFromConfig.
	Schema string

	// Service names the emitting service; bound to every SchemaV2 line as "service".
	// Ignored under SchemaV1.
	Service string

	// Env names the deploy environment; bound to every SchemaV2 line as "env".
	// Ignored under SchemaV1.
	Env string

	// Kind specifies which logger implementation to use.
	Kind Kind

	// RedactPII enables automatic PII scrubbing.
	// Only used when Kind is KindZap, ignored for KindStdlib.
	RedactPII bool

	// AddSource enables source code location in log records.
	// Only used when Kind is KindStdlib, ignored for KindZap; SchemaV2 forces it on.
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
		Schema:    SchemaV1,
		Service:   "",
		Env:       "",
	}
}

// ToOptions converts this Config to a slice of Option functions.
// Useful for converting configs to builder options.
func (c *Config) ToOptions() []options.Option[Config] {
	snapshot := *c

	return []options.Option[Config]{
		func(target *Config) { *target = snapshot },
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

// WithSchema selects the log schema (SchemaV1 or SchemaV2).
func WithSchema(schema string) options.Option[Config] {
	return func(c *Config) {
		c.Schema = schema
	}
}

// WithService sets the service name SchemaV2 binds to every line.
func WithService(service string) options.Option[Config] {
	return func(c *Config) {
		c.Service = service
	}
}

// WithEnv sets the deploy environment SchemaV2 binds to every line.
func WithEnv(env string) options.Option[Config] {
	return func(c *Config) {
		c.Env = env
	}
}
