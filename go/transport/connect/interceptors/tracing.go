package interceptors

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TraceResponseHeader is the response header carrying the active span's W3C trace
// context back to the caller, so a client or E2E test can correlate its
// request to the spans/metrics/logs it produced. Its value is in traceparent format
// (`00-<trace-id>-<span-id>-<flags>`).
const TraceResponseHeader = "Traceresponse"

// setTraceResponse stamps the active trace context (traceparent format) onto header
// under TraceResponseHeader, or does nothing when ctx carries no valid span context — so
// the header is never present with an empty/invalid value.
func setTraceResponse(ctx context.Context, header *connect.Header) {
	// Serialize the active span context to a W3C traceparent using the TraceContext
	// propagator directly (not the global one) so the value is deterministic regardless
	// of process-wide propagator setup — mirroring foundation/tracer.TraceParentFromContext.
	// Empty when ctx carries no valid span context.
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)

	if tp := carrier["traceparent"]; tp != "" {
		header.Set(TraceResponseHeader, tp)
	}
}

// TracingInterceptor creates a server-side interceptor that traces every Connect RPC —
// unary and streaming alike, one server span per RPC — using the interfaces.Tracer
// abstraction. It extracts W3C Trace Context from the request header to parent the span,
// records X-Request-ID as a span attribute, and stamps TraceResponseHeader on the
// response header before the handler runs, since response headers flush with the first
// Send (so a failed RPC carries it too). The client trace path is the client tier
// (clients/rpc/connect/interceptors).
func TracingInterceptor(tracer interfaces.Tracer) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(
			ctx context.Context,
			spec connect.Spec,
			stream connect.ServerStream,
		) error {
			info := callInfo(ctx)

			ctx, span := startServerSpan(
				ctx,
				tracer,
				info.RequestHeader(),
				spec.Procedure,
			)
			defer span.End()

			setTraceResponse(ctx, info.ResponseHeader())

			err := next(ctx, spec, stream)
			finishSpan(span, err)

			return err
		}
	}
}

// finishSpan records the terminal status/error on the span.
func finishSpan(span interfaces.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	} else {
		span.SetStatus(interfaces.SpanStatusOK, "")
	}
}

// startServerSpan extracts the incoming trace context, opens a server span for the
// procedure, and sets the rpc.* + request-id attributes. The caller must span.End().
func startServerSpan(
	ctx context.Context,
	tracer interfaces.Tracer,
	header *connect.Header,
	procedure string,
) (context.Context, interfaces.Span) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, wire.HeaderCarrier{Header: header})

	service, method := parseConnectProcedure(procedure)

	ctx, span := tracer.Start(
		ctx, procedure,
		interfaces.WithSpanKind(interfaces.SpanKindServer),
	)
	span.SetAttribute("rpc.system", "connect")
	span.SetAttribute("rpc.service", service)
	span.SetAttribute("rpc.method", method)

	if requestID := header.Get("X-Request-ID"); requestID != "" {
		span.SetAttribute("http.request_id", requestID)
	}

	return ctx, span
}

// parseConnectProcedure extracts service and method from a Connect procedure.
// Connect procedures have the format "/package.ServiceName/MethodName".
func parseConnectProcedure(procedure string) (service, method string) {
	// A Connect procedure is "/package.Service/Method". Slice it in place — the
	// first '/' separates service from method — instead of allocating a []string
	// via strings.Split on this per-request tracing/metrics hot path.
	p := strings.TrimPrefix(procedure, "/")
	if before, after, ok := strings.Cut(p, "/"); ok {
		return before, after
	}

	return "", procedure
}
