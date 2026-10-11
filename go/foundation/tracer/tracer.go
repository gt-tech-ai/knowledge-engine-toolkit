// Package tracer provides distributed tracing with swappable backends.
//
// Use New() or NewFromConfig() to create a tracer instance. The factory pattern
// allows selecting between tracing backends at runtime.
//
// Unlike other foundation packages, New() and NewFromConfig() require a
// context.Context parameter because the OTel OTLP exporter setup needs one
// for connection establishment.
//
// Example:
//
//	t, err := tracer.New(ctx, tracer.KindOTel,
//	    tracer.WithServiceName("my-service"),
//	    tracer.WithEndpoint("localhost:4317"),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer t.Shutdown(ctx)
package tracer

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/nooptracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/oteltracer"
)

// TraceParentFromContext returns the W3C traceparent header for the active span in
// ctx, or "" when there is no active span context. It is used to stamp domain
// events (e.g. the document-uploaded outbox payload) with the originating trace so
// a downstream consumer can continue the same distributed trace.
func TraceParentFromContext(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier["traceparent"]
}

// TraceIDFromContext returns the 32-hex W3C trace id of the active span in ctx, or ""
// when there is no valid span context. It is the bare trace id (not the full
// traceparent), used to return the trace to a client that cannot read a response header —
// e.g. the WebSocket query response's trace_id field, which a browser correlates against
// Tempo. The trace id is the second dash-delimited field of the traceparent
// ("00-<trace-id>-<span-id>-<flags>").
func TraceIDFromContext(ctx context.Context) string {
	parts := strings.SplitN(TraceParentFromContext(ctx), "-", 4)
	if len(parts) >= 2 && len(parts[1]) == 32 {
		return parts[1]
	}
	return ""
}

// ContextWithForcedSample returns ctx seeded with a fresh, sampled REMOTE parent span
// context, so a subsequent Tracer.Start under a ParentBased sampler records the span
// regardless of the configured sample ratio. It force-samples a server-created ROOT span
// that has no client-supplied traceparent to honour — e.g. a WebSocket message handler,
// whose browser client cannot stamp a per-frame traceparent — such as an end-to-end
// telemetry check. The ids are crypto-random so the forced trace never collides with a
// real one; on the (practically impossible) rand failure it returns ctx unchanged,
// degrading to the configured ratio rather than erroring on a request path.
func ContextWithForcedSample(ctx context.Context) context.Context {
	var tid oteltrace.TraceID
	var sid oteltrace.SpanID
	if _, err := rand.Read(tid[:]); err != nil {
		return ctx
	}
	if _, err := rand.Read(sid[:]); err != nil {
		return ctx
	}
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	return oteltrace.ContextWithRemoteSpanContext(ctx, sc)
}

// normalizeEndpoint strips any URL scheme so the value is the bare host:port the
// OTLP gRPC exporter's WithEndpoint expects. A scheme'd value (e.g. the
// OTEL_EXPORTER_OTLP_ENDPOINT convention "http://alloy.monitoring:4317") would
// otherwise produce a "too many colons in address" gRPC dial error.
func normalizeEndpoint(endpoint string) string {
	if i := strings.Index(endpoint, "://"); i != -1 {
		endpoint = endpoint[i+3:]
	}
	return strings.TrimSuffix(endpoint, "/")
}

// Kind specifies which tracer implementation to use.
type Kind int

const (
	// KindOTel uses OpenTelemetry with OTLP gRPC exporter.
	// Suitable for production with distributed tracing backends.
	KindOTel Kind = iota

	// KindNoop is a no-op tracer that discards all spans.
	// Use it when tracing is disabled or the OTel collector is unavailable.
	KindNoop
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindOTel:
		return "otel"
	case KindNoop:
		return "noop"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// New creates a Tracer of the specified kind with optional functional options.
// Takes a context.Context because the OTel exporter setup requires one.
// Returns an error if the kind is unknown or OTel setup fails.
func New(
	ctx context.Context,
	kind Kind,
	opts ...options.Option[Config],
) (interfaces.Tracer, error) {
	cfg := DefaultConfig("unnamed-service")
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(ctx, cfg)
}

// NewFromConfig creates a Tracer from a Config struct.
// Returns an error if the kind is unknown or OTel setup fails.
func NewFromConfig(ctx context.Context, cfg Config) (interfaces.Tracer, error) {
	switch cfg.Kind {
	case KindOTel:
		otelCfg := oteltracer.Config{
			ServiceName: cfg.ServiceName,
			Endpoint:    normalizeEndpoint(cfg.Endpoint),
			SampleRate:  cfg.SampleRate,
			Insecure:    cfg.Insecure,
		}
		return oteltracer.New(ctx, otelCfg)

	case KindNoop:
		return nooptracer.New()

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown tracer kind: %v", cfg.Kind),
		)
	}
}
