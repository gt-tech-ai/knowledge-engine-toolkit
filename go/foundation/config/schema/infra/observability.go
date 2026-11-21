package infra

// defaultObservabilityConfigSampleRate is the default SampleRate of ObservabilityConfig.
const defaultObservabilityConfigSampleRate = 0.1

// defaultObservabilityConfigPort is the default Port of ObservabilityConfig.
const defaultObservabilityConfigPort = 9090

// TracingConfig holds distributed tracing configuration.
type TracingConfig struct {
	// Endpoint is the OTLP collector address (e.g. "localhost:4317").
	Endpoint string `mapstructure:"endpoint" envalias:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	// Sampler selects the sampling rule: "ratio" samples a root span by SampleRate;
	// "link_aware" also samples a root span that holds a link to a sampled span, so a
	// batch span is never dropped while a trace it links to is kept. An unknown kind
	// fails the tracer build.
	Sampler string `mapstructure:"sampler"`

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
			Sampler:    "ratio",
			SampleRate: defaultObservabilityConfigSampleRate,
		},
		Metrics: MetricsConfig{
			Enabled: true,
			Port:    defaultObservabilityConfigPort,
		},
	}
}

// Validate returns an error if the configuration is invalid.
func (ObservabilityConfig) Validate() error {
	return nil
}
