package middleware

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTPTracing starts a SERVER span per HTTP request, propagating inbound trace
// context. This makes plain HTTP/REST traffic (not just Connect RPCs) part of
// the distributed trace and, crucially, puts a span in the request context so
// downstream logging (HTTPMetrics) can attach trace_id/span_id. Place it before
// HTTPMetrics in the chain. Sampling is governed by the global tracer provider.
func HTTPTracing() Middleware {
	tracer := otel.Tracer("http.server")
	// httpPropagator extracts inbound W3C trace context (traceparent) + baggage so
	// HTTP requests join an existing distributed trace.
	httpPropagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := httpPropagator.Extract(
				r.Context(),
				propagation.HeaderCarrier(r.Header),
			)
			ctx, span := tracer.Start(
				ctx,
				r.Method+" "+routeLabel(r),
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("url.path", r.URL.Path),
				),
			)
			defer span.End()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
