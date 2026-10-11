package interfaces

import "context"

// Tracer provides distributed tracing capabilities.
//
// Implementations: OpenTelemetry (default), or any tracing backend
// that supports span creation and context propagation.
//
// All foundation and service code depends on this interface, never on a
// concrete tracing library. Swap implementations via the tracer.New factory.
type Tracer interface {
	// Start creates a new span and returns the updated context and span.
	Start(ctx context.Context, name string, opts ...SpanOption) (context.Context, Span)

	// Shutdown flushes and shuts down the tracer.
	Shutdown(ctx context.Context) error
}

// Span represents a single operation within a trace.
type Span interface {
	// End completes the span.
	End()

	// SetAttribute adds a key-value attribute to the span.
	SetAttribute(key string, value any)

	// RecordError records an error on the span.
	RecordError(err error)

	// SetStatus sets the span status.
	SetStatus(code SpanStatusCode, description string)
}

// SpanOption configures span creation.
type SpanOption func(*SpanConfig)

// SpanConfig holds span creation configuration.
type SpanConfig struct {
	// Kind specifies the span kind (client, server, producer, consumer, internal).
	Kind SpanKind
}

// SpanKind identifies the role of a span in a trace.
type SpanKind int

const (
	// SpanKindInternal is the default span kind, for work internal to a service.
	SpanKindInternal SpanKind = iota
	// SpanKindServer marks a span handling an inbound request.
	SpanKindServer
	// SpanKindClient marks a span issuing an outbound request.
	SpanKindClient
	// SpanKindProducer marks a span publishing a message to a broker.
	SpanKindProducer
	// SpanKindConsumer marks a span consuming a message from a broker.
	SpanKindConsumer
)

// SpanStatusCode represents the status of a span.
type SpanStatusCode int

const (
	// SpanStatusUnset is the default status, leaving the span outcome unspecified.
	SpanStatusUnset SpanStatusCode = iota
	// SpanStatusOK marks the span's operation as successful.
	SpanStatusOK
	// SpanStatusError marks the span's operation as failed.
	SpanStatusError
)

// WithSpanKind returns a SpanOption that sets the span kind.
func WithSpanKind(kind SpanKind) SpanOption {
	return func(cfg *SpanConfig) {
		cfg.Kind = kind
	}
}
