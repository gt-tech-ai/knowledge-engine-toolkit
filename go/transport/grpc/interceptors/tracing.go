package interceptors

import (
	"context"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TracingServerInterceptor returns a server-side interceptor that continues the
// caller's trace — the W3C trace context its client injected into the incoming
// metadata — and opens a server span per RPC, named by its full method and carrying
// the rpc.system, rpc.service and rpc.method attributes.
func TracingServerInterceptor(tracer interfaces.Tracer) grpc.UnaryServerInterceptor {
	return unary(tracing(tracer))
}

// TracingStreamServerInterceptor is TracingServerInterceptor for streaming RPCs: one
// server span covers the whole stream.
func TracingStreamServerInterceptor(
	tracer interfaces.Tracer,
) grpc.StreamServerInterceptor {
	return stream(tracing(tracer))
}

// tracing runs an RPC in a server span continuing the caller's trace.
func tracing(tracer interfaces.Tracer) around {
	return func(
		ctx context.Context, fullMethod string, next func(context.Context) error,
	) error {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			ctx = otel.GetTextMapPropagator().Extract(ctx, wire.MetadataCarrier{MD: md})
		}
		service, method := parseGRPCMethod(fullMethod)
		ctx, span := tracer.Start(
			ctx, fullMethod,
			interfaces.WithSpanKind(interfaces.SpanKindServer),
		)
		defer span.End()
		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.service", service)
		span.SetAttribute("rpc.method", method)
		err := next(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(interfaces.SpanStatusError, err.Error())
		} else {
			span.SetStatus(interfaces.SpanStatusOK, "")
		}
		return err
	}
}
