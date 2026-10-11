package tracer

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"

// Config is the superset configuration for all tracer kinds.
type Config struct {
	// ServiceName identifies this service in distributed traces.
	ServiceName string

	// Endpoint is the OTLP gRPC collector address in host:port format.
	// Only used when Kind is KindOTel.
	Endpoint string

	// Kind specifies which tracer implementation to use.
	Kind Kind

	// SampleRate controls the fraction of traces sampled (0.0 to 1.0).
	// Only used when Kind is KindOTel.
	SampleRate float64

	// Insecure disables TLS for the OTLP gRPC connection.
	// Only used when Kind is KindOTel.
	Insecure bool
}

// DefaultConfig returns the default tracer configuration with KindOTel.
func DefaultConfig(serviceName string) Config {
	return Config{
		Kind:        KindOTel,
		ServiceName: serviceName,
		Endpoint:    "localhost:4317",
		SampleRate:  1.0,
		Insecure:    true,
	}
}

// ToOptions converts this Config to a slice of Option functions.
func (c Config) ToOptions() []options.Option[Config] {
	return []options.Option[Config]{
		func(target *Config) { *target = c },
	}
}

// WithServiceName sets the service name for distributed tracing.
func WithServiceName(name string) options.Option[Config] {
	return func(c *Config) {
		c.ServiceName = name
	}
}

// WithEndpoint sets the OTLP gRPC collector endpoint.
func WithEndpoint(endpoint string) options.Option[Config] {
	return func(c *Config) {
		c.Endpoint = endpoint
	}
}

// WithSampleRate sets the trace sampling rate (0.0 to 1.0).
func WithSampleRate(rate float64) options.Option[Config] {
	return func(c *Config) {
		c.SampleRate = rate
	}
}

// WithInsecure disables TLS for the OTLP gRPC connection.
func WithInsecure(insecure bool) options.Option[Config] {
	return func(c *Config) {
		c.Insecure = insecure
	}
}
