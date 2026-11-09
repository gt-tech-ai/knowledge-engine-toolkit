package interceptors

import (
	"context"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc/metadata"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
)

// injectTraceContext returns ctx with the active span's W3C trace context injected
// into a copy of its outgoing gRPC metadata, so the downstream server continues this
// trace (its server interceptor extracts the traceparent) instead of starting a new,
// disconnected one.
func injectTraceContext(ctx context.Context) context.Context {
	value, ok := metadata.FromOutgoingContext(ctx)
	if ok {
		// Copy so we never mutate the caller's outgoing MD.
		value = value.Copy()
	} else {
		value = metadata.MD{}
	}

	otel.GetTextMapPropagator().Inject(ctx, wire.MetadataCarrier{MD: value})

	return metadata.NewOutgoingContext(ctx, value)
}
