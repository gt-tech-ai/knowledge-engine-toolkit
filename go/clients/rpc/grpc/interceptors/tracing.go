package interceptors

import (
	"context"
	"io"
	"sync"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// grpcMetadataCarrier adapts gRPC metadata.MD to the OTel TextMapCarrier so the
// global propagator can inject W3C trace context (traceparent) over gRPC metadata,
// the gRPC analog of propagation.HeaderCarrier for HTTP/Connect.
//
// DRY note: this is the gRPC sibling of the Connect side's propagation.HeaderCarrier. If
// a third transport ever needs W3C-over-headers propagation, promote the two into a
// shared carrier helper rather than growing a third copy that can drift.
type grpcMetadataCarrier struct {
	// md is the wrapped gRPC metadata the trace context is read from / injected into.
	md metadata.MD
}

// Get returns the first value for key (metadata keys are lower-cased by gRPC).
func (c grpcMetadataCarrier) Get(key string) string {
	if v := c.md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// Set stores value under key, replacing any existing values.
func (c grpcMetadataCarrier) Set(key, value string) { c.md.Set(key, value) }

// Keys lists the carrier's metadata keys.
func (c grpcMetadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c.md))
	for k := range c.md {
		keys = append(keys, k)
	}
	return keys
}

// injectTraceContext returns ctx with the active span's W3C trace context injected
// into a copy of its outgoing gRPC metadata, so the downstream server continues this
// trace (its server interceptor extracts the traceparent) instead of starting a new,
// disconnected one. Without this a client span is created but never propagated on the
// wire, so a single request fragments into one trace per service.
func injectTraceContext(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if ok {
		// Copy so we never mutate the caller's outgoing MD. This is a per-call allocation
		// on the request hot path; revisit (e.g. a pooled carrier) if a high-QPS caller
		// makes it show up in a profile.
		md = md.Copy()
	} else {
		md = metadata.MD{}
	}
	otel.GetTextMapPropagator().Inject(ctx, grpcMetadataCarrier{md: md})
	return metadata.NewOutgoingContext(ctx, md)
}

// TracingServerInterceptor returns a server-side interceptor that creates a
// span for each incoming RPC using the provided Tracer.
func TracingServerInterceptor(tracer interfaces.Tracer) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		service, method := parseGRPCMethod(info.FullMethod)

		ctx, span := tracer.Start(
			ctx, info.FullMethod,
			interfaces.WithSpanKind(interfaces.SpanKindServer),
		)
		defer span.End()

		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.service", service)
		span.SetAttribute("rpc.method", method)

		resp, err := handler(ctx, req)

		if err != nil {
			span.RecordError(err)
			span.SetStatus(interfaces.SpanStatusError, err.Error())
		} else {
			span.SetStatus(interfaces.SpanStatusOK, "")
		}

		return resp, err
	}
}

// TracingClientInterceptor returns a client-side interceptor that creates a
// span for each outgoing RPC using the provided Tracer.
func TracingClientInterceptor(tracer interfaces.Tracer) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		service, rpcMethod := parseGRPCMethod(method)

		ctx, span := tracer.Start(
			ctx, method,
			interfaces.WithSpanKind(interfaces.SpanKindClient),
		)
		defer span.End()

		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.service", service)
		span.SetAttribute("rpc.method", rpcMethod)

		// Propagate the trace over the wire so the callee continues this trace.
		ctx = injectTraceContext(ctx)

		err := invoker(ctx, method, req, reply, cc, opts...)

		if err != nil {
			span.RecordError(err)
			span.SetStatus(interfaces.SpanStatusError, status.Convert(err).Message())
		} else {
			span.SetStatus(interfaces.SpanStatusOK, "")
		}

		return err
	}
}

// TracingStreamClientInterceptor returns a client-side stream interceptor
// that creates a span for each outgoing stream using the provided Tracer.
// The span stays open for the full stream lifetime and is ended once the
// stream finishes — observed via a terminal RecvMsg, or via a background
// watch of ctx for a caller that cancels the stream and does not drain it
// (see tracingClientStream.watch).
func TracingStreamClientInterceptor(
	tracer interfaces.Tracer,
) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		service, rpcMethod := parseGRPCMethod(method)

		ctx, span := tracer.Start(
			ctx, method,
			interfaces.WithSpanKind(interfaces.SpanKindClient),
		)
		span.SetAttribute("rpc.system", "grpc")
		span.SetAttribute("rpc.service", service)
		span.SetAttribute("rpc.method", rpcMethod)

		// Propagate the trace over the wire so the streaming callee (e.g. a
		// server-streaming RPC) continues this trace instead of starting a new one.
		ctx = injectTraceContext(ctx)

		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(interfaces.SpanStatusError, status.Convert(err).Message())
			span.End()
			return nil, err
		}

		wrapped := &tracingClientStream{
			ClientStream: stream,
			span:         span,
			done:         make(chan struct{}),
		}
		go wrapped.watch(ctx)
		return wrapped, nil
	}
}

// tracingClientStream wraps a grpc.ClientStream to end its span exactly once
// when the stream finishes.
type tracingClientStream struct {
	// ClientStream is the wrapped gRPC client stream.
	grpc.ClientStream
	// span is the stream's span, ended once when the stream finishes.
	span interfaces.Span
	// done is closed once the stream has been finalized, coordinating watch and RecvMsg.
	done chan struct{}
	// once guarantees the span is ended exactly once.
	once sync.Once
}

// RecvMsg implements grpc.ClientStream, ending the span exactly once when the
// stream terminates (io.EOF on clean completion, or an error).
func (s *tracingClientStream) RecvMsg(m any) error {
	err := s.ClientStream.RecvMsg(m)
	if err != nil {
		s.finish(err)
	}
	return err
}

// watch ends the span as canceled/failed as soon as ctx ends, without
// waiting for a RecvMsg call that may never come (e.g. a caller that cancels
// the stream and abandons it without draining). ctx is the caller's own call
// context, unwrapped by this interceptor, so it only ends on a genuine
// cancellation or deadline — never as a side effect of the stream completing
// normally, which would otherwise race a healthy stream's own terminal
// RecvMsg. watch exits without effect once finish has already run via
// RecvMsg.
//
// ctx cancellation and a natural terminal RecvMsg can fire at nearly the same
// instant; the outer select would then pick pseudo-randomly and could end the
// span as canceled/deadline-exceeded for a healthy, fully-drained stream. So
// on ctx.Done() watch re-checks s.done before ending the span: a RecvMsg that
// has already finalized (the authoritative outcome) wins, and finish's
// sync.Once keeps the End() single-emit even if the two truly interleave.
// Finalization is keyed on RecvMsg, which is correct for server-streaming
// (this package's only use); a client-streaming/bidi SendMsg failure would not
// finalize until ctx ends.
func (s *tracingClientStream) watch(ctx context.Context) {
	select {
	case <-ctx.Done():
		select {
		case <-s.done: // a terminal RecvMsg already won — defer to its outcome
		default:
			s.finish(status.FromContextError(ctx.Err()).Err())
		}
	case <-s.done:
	}
}

// finish ends the stream's span exactly once (recording err unless it is a clean
// io.EOF completion) and closes done.
func (s *tracingClientStream) finish(err error) {
	s.once.Do(func() {
		defer close(s.done)
		if apperr.StdIs(err, io.EOF) {
			s.span.SetStatus(interfaces.SpanStatusOK, "")
		} else {
			s.span.RecordError(err)
			s.span.SetStatus(interfaces.SpanStatusError, status.Convert(err).Message())
		}
		s.span.End()
	})
}
