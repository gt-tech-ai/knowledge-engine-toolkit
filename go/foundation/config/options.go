package config

import (
	"maps"
	"slices"

	viperloader "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/viper"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Config is the top-level configuration for the config loader factory.
type Config struct {
	// Viper holds Viper-specific configuration.
	Viper ViperConfig

	// Kind specifies which config loader implementation to use.
	Kind Kind
}

// ViperConfig is the Viper backend's configuration: an alias of viper.Config, so
// every loader field (Schema, ExtraEnv, BaseDir, Env, Prefix) is settable through
// the factory without a hand-kept copy. See viper.Config for each field's trade-offs.
type ViperConfig = viperloader.Config

// DefaultConfig returns the default config with KindViper and sensible defaults.
func DefaultConfig() Config {
	return Config{
		Kind: KindViper,
		Viper: ViperConfig{
			BaseDir: ".",
			Env:     viperloader.ResolveEnv(),
		},
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithBaseDir sets the directory containing config files.
func WithBaseDir(dir string) options.Option[Config] {
	return func(c *Config) { c.Viper.BaseDir = dir }
}

// WithEnvironment sets the environment name for overlay loading.
func WithEnvironment(env string) options.Option[Config] {
	return func(c *Config) { c.Viper.Env = env }
}

// WithEnvSelectors picks the overlay from the consumer's own env vars: the first
// non-empty one among selectors, trimmed and lowercased (see viper.ResolveEnvFrom),
// instead of the default APP_ENV then ENVIRONMENT. It is resolved when the option is
// applied; none set means base.yaml alone.
func WithEnvSelectors(selectors ...string) options.Option[Config] {
	return func(c *Config) { c.Viper.Env = viperloader.ResolveEnvFrom(selectors...) }
}

// WithEnvPrefix sets the prefix for environment variable binding (default: none).
// Set one: with no prefix, env vars are read by their bare names, which collide with
// the <SVC>_PORT / <SVC>_SERVICE_HOST variables Kubernetes injects for each Service.
func WithEnvPrefix(prefix string) options.Option[Config] {
	return func(c *Config) { c.Viper.Prefix = prefix }
}

// WithSchema sets the consumer's root config struct (or a pointer to one) used to
// derive env bindings. Without a schema, UnmarshalKey sees env overrides only for
// keys present in the YAML or listed in WithExtraEnv.
func WithSchema(root any) options.Option[Config] {
	return func(c *Config) { c.Viper.Schema = root }
}

// WithExtraEnv binds config keys to env var names (key → names, first set wins),
// for keys outside the schema or to replace a schema key's derived binding (see
// viper.Config.ExtraEnv). Repeated calls merge: each adds its keys, and a later
// entry for the same key replaces the earlier one. The caller's map is copied.
func WithExtraEnv(bindings map[string][]string) options.Option[Config] {
	return func(c *Config) {
		merged := make(map[string][]string, len(c.Viper.ExtraEnv)+len(bindings))
		maps.Copy(merged, c.Viper.ExtraEnv)
		for key, names := range bindings {
			merged[key] = slices.Clone(names)
		}
		c.Viper.ExtraEnv = merged
	}
}
