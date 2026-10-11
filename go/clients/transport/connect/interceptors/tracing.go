package interceptors

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TraceResponseHeader is the response header carrying the active span's W3C trace
// context back to the caller, so a client or E2E test can correlate its
// request to the spans/metrics/logs it produced. Its value is in traceparent format
// (`00-<trace-id>-<span-id>-<flags>`).
const TraceResponseHeader = "traceresponse"

// setTraceResponse stamps the active trace context (traceparent format) onto h under
// TraceResponseHeader, or does nothing when ctx carries no valid span context — so the
// header is never present with an empty/invalid value.
func setTraceResponse(ctx context.Context, h http.Header) {
	// Serialize the active span context to a W3C traceparent using the TraceContext
	// propagator directly (not the global one) so the value is deterministic regardless
	// of process-wide propagator setup — mirroring foundation/tracer.TraceParentFromContext.
	// Empty when ctx carries no valid span context.
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	if tp := carrier["traceparent"]; tp != "" {
		h.Set(TraceResponseHeader, tp)
	}
}

// tracingInterceptor traces Connect RPC calls — unary and server-streaming — with the
// interfaces.Tracer abstraction. It extracts W3C Trace Context from incoming headers to
// parent spans correctly and records X-Request-ID as a span attribute. Server-side only
// (WrapStreamingClient is a no-op; the client path is ClientTracingInterceptor).
type tracingInterceptor struct {
	// tracer creates the per-RPC span (parented from incoming W3C trace context).
	tracer interfaces.Tracer
}

// TracingInterceptor creates a server-side interceptor that traces Connect RPC calls
// (unary and streaming) using the interfaces.Tracer abstraction. It extracts W3C Trace
// Context from incoming headers to parent spans correctly and records X-Request-ID as a
// span attribute.
func TracingInterceptor(tracer interfaces.Tracer) connect.Interceptor {
	return tracingInterceptor{tracer: tracer}
}

// startServerSpan extracts the incoming trace context, opens a server span for the
// procedure, and sets the rpc.* + request-id attributes. Shared by the unary and
// streaming paths. The caller must span.End().
func (i tracingInterceptor) startServerSpan(
	ctx context.Context, h http.Header, procedure string,
) (context.Context, interfaces.Span) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(h))
	service, method := parseConnectProcedure(procedure)

	ctx, span := i.tracer.Start(
		ctx, procedure,
		interfaces.WithSpanKind(interfaces.SpanKindServer),
	)
	span.SetAttribute("rpc.system", "connect")
	span.SetAttribute("rpc.service", service)
	span.SetAttribute("rpc.method", method)
	if requestID := h.Get("X-Request-ID"); requestID != "" {
		span.SetAttribute("http.request_id", requestID)
	}
	return ctx, span
}

// finishSpan records the terminal status/error on the span. Shared by both paths.
func finishSpan(span interfaces.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(interfaces.SpanStatusError, err.Error())
	} else {
		span.SetStatus(interfaces.SpanStatusOK, "")
	}
}

// WrapUnary traces a unary RPC.
func (i tracingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, span := i.startServerSpan(ctx, req.Header(), req.Spec().Procedure)
		defer span.End()
		resp, err := next(ctx, req)
		finishSpan(span, err)
		// Return the trace id to the caller. Only on success: the error path
		// returns a nil response, which has no header map to stamp.
		if err == nil && resp != nil {
			setTraceResponse(ctx, resp.Header())
		}
		return resp, err
	}
}

// WrapStreamingClient is a no-op — the client trace path is ClientTracingInterceptor.
func (i tracingInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler opens one server span spanning the whole server-streaming RPC.
func (i tracingInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, span := i.startServerSpan(ctx, conn.RequestHeader(), conn.Spec().Procedure)
		defer span.End()
		// Stamp the trace id on the response header BEFORE next(): streaming response
		// headers flush with the first Send, so setting it afterwards would be too late
		//
		setTraceResponse(ctx, conn.ResponseHeader())
		err := next(ctx, conn)
		finishSpan(span, err)
		return err
	}
}

// ClientTracingInterceptor creates a client-side interceptor that traces Connect RPC
// calls with SpanKindClient and injects W3C Trace Context into outgoing request headers.
func ClientTracingInterceptor(tracer interfaces.Tracer) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			spec := req.Spec()
			procedure := spec.Procedure
			service, method := parseConnectProcedure(procedure)

			ctx, span := tracer.Start(
				ctx, procedure,
				interfaces.WithSpanKind(interfaces.SpanKindClient),
			)
			defer span.End()

			span.SetAttribute("rpc.system", "connect")
			span.SetAttribute("rpc.service", service)
			span.SetAttribute("rpc.method", method)

			// Inject trace context into outgoing headers using the global propagator
			otel.GetTextMapPropagator().
				Inject(ctx, propagation.HeaderCarrier(req.Header()))

			resp, err := next(ctx, req)

			if err != nil {
				span.RecordError(err)
				span.SetStatus(interfaces.SpanStatusError, err.Error())
			} else {
				span.SetStatus(interfaces.SpanStatusOK, "")
			}

			return resp, err
		}
	}
}

// parseConnectProcedure extracts service and method from a Connect procedure.
// Connect procedures have the format "/package.ServiceName/MethodName".
func parseConnectProcedure(procedure string) (service, method string) {
	// A Connect procedure is "/package.Service/Method". Slice it in place — the
	// first '/' separates service from method — instead of allocating a []string
	// via strings.Split on this per-request tracing/metrics hot path.
	p := strings.TrimPrefix(procedure, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", procedure
}
