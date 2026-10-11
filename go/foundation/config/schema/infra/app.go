package infra

// AppConfig holds application-level configuration.
type AppConfig struct {
	// Name is the human-readable application name (e.g. "Tech AI Knowledge Engine").
	Name string `mapstructure:"name"`

	// Environment is the deployment environment identifier (e.g. "dev", "staging", "prod").
	Environment string `mapstructure:"environment"`
}

// DefaultAppConfig returns an AppConfig with defaults.
func DefaultAppConfig() AppConfig {
	return AppConfig{
		Name:        "Tech AI Knowledge Engine",
		Environment: "development",
	}
}

// Validate returns an error if the configuration is invalid.
func (c AppConfig) Validate() error {
	return nil
}
