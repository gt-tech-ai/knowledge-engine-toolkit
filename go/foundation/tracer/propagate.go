package tracer

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Inject writes ctx's propagated context (the W3C traceparent / tracestate and the
// baggage the process propagator carries) into carrier, a message's string headers, so
// a consumer can continue the producer's trace. A nil carrier is a no-op.
func Inject(ctx context.Context, carrier map[string]string) {
	if carrier == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(carrier))
}

// Extract returns ctx carrying the propagated context Inject wrote into carrier, so the
// consumer's spans continue the producer's trace; ctx is returned unchanged when
// carrier holds none.
func Extract(ctx context.Context, carrier map[string]string) context.Context {
	if len(carrier) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(carrier))
}
