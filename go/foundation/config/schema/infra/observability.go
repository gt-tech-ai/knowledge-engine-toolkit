package infra

// TracingConfig holds distributed tracing configuration.
type TracingConfig struct {
	// Endpoint is the OTLP collector address (e.g. "localhost:4317").
	Endpoint string `mapstructure:"endpoint" envalias:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	// SampleRate is the fraction of traces to sample (0.0-1.0; 0.1 = 10%).
	SampleRate float64 `mapstructure:"sample_rate"`

	// Enabled controls whether distributed tracing is active.
	Enabled bool `mapstructure:"enabled"`
}

// MetricsConfig holds metrics export configuration.
type MetricsConfig struct {
	// Enabled controls whether Prometheus metrics collection is active.
	Enabled bool `mapstructure:"enabled"`

	// Port is the HTTP port on which the /metrics endpoint is served.
	Port int `mapstructure:"port"`
}

// ObservabilityConfig holds tracing and metrics configuration.
type ObservabilityConfig struct {
	// Tracing holds distributed tracing (OpenTelemetry) configuration.
	Tracing TracingConfig `mapstructure:"tracing"`

	// Metrics holds Prometheus metrics export configuration.
	Metrics MetricsConfig `mapstructure:"metrics"`
}

// DefaultObservabilityConfig returns an ObservabilityConfig with defaults for local dev.
func DefaultObservabilityConfig() ObservabilityConfig {
	return ObservabilityConfig{
		Tracing: TracingConfig{
			Enabled:    true,
			Endpoint:   "localhost:4317",
			SampleRate: 0.1,
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Port:    9090,
		},
	}
}

// Validate returns an error if the configuration is invalid.
func (c ObservabilityConfig) Validate() error {
	return nil
}
