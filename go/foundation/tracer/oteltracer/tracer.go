// Package oteltracer provides an OpenTelemetry-backed tracer implementation.
package oteltracer

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertions.
var (
	// Tracer must satisfy interfaces.Tracer.
	_ interfaces.Tracer = (*Tracer)(nil)
	// span must satisfy interfaces.Span.
	_ interfaces.Span = (*span)(nil)
)

// Config holds OTel-specific tracer configuration.
type Config struct {
	// ServiceName identifies this service in distributed traces.
	ServiceName string

	// Endpoint is the OTLP gRPC collector address in host:port format.
	Endpoint string

	// SampleRate controls the fraction of traces sampled (0.0 to 1.0; 1.0 = always).
	SampleRate float64

	// Insecure disables TLS for the OTLP gRPC connection.
	Insecure bool
}

// Tracer implements interfaces.Tracer backed by OpenTelemetry.
type Tracer struct {
	// inner is the named OTel tracer that creates spans for this service.
	inner trace.Tracer

	// shutdownFn flushes and tears down the tracer provider; called by Shutdown.
	shutdownFn func(context.Context) error
}

// New initializes OpenTelemetry and returns an interfaces.Tracer.
func New(ctx context.Context, cfg Config) (*Tracer, error) {
	tp, err := setup(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// The tracer comes from this provider, not the global one: another New may install
	// its provider between setup and here, and this tracer must keep its own sampler.
	return &Tracer{
		inner:      tp.Tracer(cfg.ServiceName),
		shutdownFn: tp.Shutdown,
	}, nil
}

// Start begins a new span named name as a child of any span in ctx, applying
// the supplied SpanOptions. The span kind is forwarded to OTel only when it
// differs from the internal default. It returns the span-bearing context and
// the span, which the caller must End.
func (t *Tracer) Start(
	ctx context.Context,
	name string,
	opts ...interfaces.SpanOption,
) (context.Context, interfaces.Span) {
	cfg := &interfaces.SpanConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	var spanOpts []trace.SpanStartOption
	if cfg.Kind != interfaces.SpanKindInternal {
		spanOpts = append(spanOpts, trace.WithSpanKind(toOTelSpanKind(cfg.Kind)))
	}

	ctx, s := t.inner.Start(ctx, name, spanOpts...)
	return ctx, &span{inner: s}
}

// Shutdown flushes buffered spans and shuts down the tracer provider, blocking
// until ctx is done. It is safe to call when no provider was installed.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t.shutdownFn != nil {
		return t.shutdownFn(ctx)
	}
	return nil
}

// span implements interfaces.Span by wrapping an OTel trace.Span and translating
// the foundation span vocabulary into OTel calls.
type span struct {
	// inner is the underlying OTel span this span delegates to.
	inner trace.Span
}

// End completes the span.
func (s *span) End() { s.inner.End() }

// SetAttribute attaches a key-value attribute to the span, converting value to
// the matching OTel attribute type.
func (s *span) SetAttribute(key string, value any) {
	s.inner.SetAttributes(toOTelAttribute(key, value))
}

// RecordError records err as an event on the span.
func (s *span) RecordError(err error) { s.inner.RecordError(err) }

// SetStatus sets the span status, mapping the foundation status code to its
// OTel equivalent.
func (s *span) SetStatus(code interfaces.SpanStatusCode, description string) {
	s.inner.SetStatus(toOTelStatusCode(code), description)
}

// setup wires the OTel SDK for cfg: it builds the OTLP gRPC exporter, the
// service resource, and the sampler, installs the global tracer provider and
// W3C TraceContext + Baggage propagators, and returns the provider so the caller
// takes its tracer from it and can flush it on exit.
func setup(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		// Tolerate a brief collector-unavailable window at startup — e.g. a pod
		// that races ahead of a node-local collector during autoscaler node
		// bring-up — by retrying transient export failures within a bounded budget
		// instead of dropping the batch and logging an error on the first miss.
		// The export timeout matches the retry budget so a retry can actually span
		// the window; both are bounded so a genuinely prolonged outage still
		// degrades gracefully (spans drop, memory stays capped) rather than
		// stalling the exporter.
		otlptracegrpc.WithTimeout(30 * time.Second),
		otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{
			Enabled:         true,
			InitialInterval: 1 * time.Second,
			MaxInterval:     5 * time.Second,
			MaxElapsedTime:  30 * time.Second,
		}),
		// Keepalive so a wedged / half-open connection is detected and re-established in
		// ~1 minute instead of stalling exports for many minutes (retry alone reuses the
		// same broken connection; keepalive PINGs during an in-flight export tear it down
		// and reconnect). PermitWithoutStream is false so pings fire only while an export
		// is active — this never trips the collector's default gRPC keepalive
		// enforcement, which only polices pings sent with no active stream.
		otlptracegrpc.WithDialOption(grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                1 * time.Minute,
			Timeout:             20 * time.Second,
			PermitWithoutStream: false,
		})),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "create OTLP exporter")
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(
			semconv.ServiceName(cfg.ServiceName),
		),
	)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "create resource")
	}

	var sampler sdktrace.Sampler
	switch {
	case cfg.SampleRate >= 1.0:
		sampler = sdktrace.AlwaysSample()
	case cfg.SampleRate <= 0:
		sampler = sdktrace.NeverSample()
	default:
		// ParentBased so an incoming sampled parent (a W3C traceparent with sampled=1,
		// extracted by the TraceContext propagator) forces the span sampled — the lever
		// an end-to-end telemetry check relies on to guarantee its one request's trace
		// reaches the backend without raising the ratio for normal traffic. A ROOT span
		// with no parent still follows the ratio.
		sampler = sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))
	}

	tp := sdktrace.NewTracerProvider(
		// A span queue larger than the SDK default (2048) so a burst on a freshly-started
		// pod — a cold exporter catching up while its connection re-establishes — buffers
		// spans instead of dropping them. Bounded so a prolonged outage still caps
		// memory.
		sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(4096)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp, nil
}

// toOTelSpanKind maps a foundation SpanKind to its OTel equivalent, defaulting
// to trace.SpanKindInternal for unrecognized values.
func toOTelSpanKind(k interfaces.SpanKind) trace.SpanKind {
	switch k {
	case interfaces.SpanKindServer:
		return trace.SpanKindServer
	case interfaces.SpanKindClient:
		return trace.SpanKindClient
	case interfaces.SpanKindProducer:
		return trace.SpanKindProducer
	case interfaces.SpanKindConsumer:
		return trace.SpanKindConsumer
	case interfaces.SpanKindInternal:
		return trace.SpanKindInternal
	default:
		return trace.SpanKindInternal
	}
}

// toOTelStatusCode maps a foundation SpanStatusCode to its OTel equivalent,
// defaulting to codes.Unset for unrecognized values.
func toOTelStatusCode(c interfaces.SpanStatusCode) codes.Code {
	switch c {
	case interfaces.SpanStatusOK:
		return codes.Ok
	case interfaces.SpanStatusError:
		return codes.Error
	case interfaces.SpanStatusUnset:
		return codes.Unset
	default:
		return codes.Unset
	}
}

// toOTelAttribute builds an OTel attribute.KeyValue for key from value, choosing
// the typed constructor that matches value's dynamic type. Unsupported types
// fall back to an empty string attribute rather than being dropped.
func toOTelAttribute(key string, value any) attribute.KeyValue {
	switch v := value.(type) {
	case string:
		return attribute.String(key, v)
	case int:
		return attribute.Int(key, v)
	case int64:
		return attribute.Int64(key, v)
	case float64:
		return attribute.Float64(key, v)
	case bool:
		return attribute.Bool(key, v)
	default:
		return attribute.String(key, "")
	}
}
