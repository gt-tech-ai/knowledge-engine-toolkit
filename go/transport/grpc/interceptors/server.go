// Package interceptors runs a gRPC server's RPCs through the controller tier
// (transport.Tier): ServerOptions lays the tier out as unary and stream server
// interceptors, with gRPC-native tracing, metrics and logging standing in for the tier's
// own.
package interceptors

import (
	"context"
	"strings"

	"google.golang.org/grpc"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
)

// Edge carries a server's own interceptors around the controller tier.
type Edge struct {
	// Authn authenticate a unary RPC's caller just before the tier's auth layer.
	Authn []grpc.UnaryServerInterceptor

	// StreamAuthn authenticate a streaming RPC's caller just before the auth layer.
	StreamAuthn []grpc.StreamServerInterceptor
}

// ServerOptions returns the server options that run every RPC through the controller
// tier as the controller named name over d (see transport.Interceptors), so its layers
// are switched and tuned by the decoration config's controllers tier:
//
//   - Tracing continues the caller's trace in a server span, metrics count requests by
//     status code, and logging writes an access line per RPC (the *ServerInterceptor
//     and *StreamServerInterceptor functions here).
//   - Recovery, the rate limit, the shedding bulkhead, auth and the timeout run the
//     tier's own middleware, the operation named by the method ("pkg.Service/Method");
//     a stream runs without the timeout, so it outlives any call budget.
//   - edge's authenticating interceptors run just before the auth layer.
//
// A handler's error reaches the caller unchanged; a rejection the tier produced itself
// (a panic, a shed request, a denial, a timeout) becomes a status of its code carrying
// only the code's client-safe message, the real error captured for the request log.
func ServerOptions(name string, d decorate.Deps, edge Edge) []grpc.ServerOption {
	var opts []grpc.ServerOption
	unaries := transport.Interceptors(name, d, nativeUnary(d), adaptUnary, edge.Authn)
	if len(unaries) > 0 {
		opts = append(opts, grpc.ChainUnaryInterceptor(unaries...))
	}
	streams := transport.Interceptors(
		name,
		d,
		nativeStream(d),
		adaptStream,
		edge.StreamAuthn,
	)
	if len(streams) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(streams...))
	}
	return opts
}

// nativeUnary returns the unary interceptors standing in for the tier's observability
// layers.
func nativeUnary(
	d decorate.Deps,
) func(decorate.Layer) ([]grpc.UnaryServerInterceptor, bool) {
	return native(d, unary)
}

// nativeStream returns the stream interceptors standing in for the tier's observability
// layers.
func nativeStream(
	d decorate.Deps,
) func(decorate.Layer) ([]grpc.StreamServerInterceptor, bool) {
	return native(d, stream)
}

// native returns, for each observability layer, the gRPC interceptor (built by shape
// from its around) standing in for it — none when its collaborator is absent — and
// reports false for every other layer, which the tier's own middleware runs.
func native[I any](
	d decorate.Deps,
	shape func(around) I,
) func(decorate.Layer) ([]I, bool) {
	return func(layer decorate.Layer) ([]I, bool) {
		switch layer {
		case decorate.LayerTracing:
			return when(
				d.Tracer != nil,
				func() around { return tracing(d.Tracer) },
				shape,
			), true
		case decorate.LayerMetrics:
			return when(
				d.Metrics != nil,
				func() around { return metrics(d.Metrics) },
				shape,
			), true
		case decorate.LayerLogging:
			return when(
				d.Logger != nil,
				func() around { return logging(d.Logger) },
				shape,
			), true
		case decorate.LayerRecovery,
			decorate.LayerAuth,
			decorate.LayerTimeout,
			decorate.LayerCircuitBreaker,
			decorate.LayerRetry,
			decorate.LayerBulkhead,
			decorate.LayerRateLimit:
			return nil, false
		default:
			return nil, false
		}
	}
}

// when returns the interceptor shape makes from build's around, or none when its
// collaborator is absent.
func when[I any](present bool, build func() around, shape func(around) I) []I {
	if !present {
		return nil
	}
	return []I{shape(build())}
}

// adaptUnary runs each unary RPC through mw.
func adaptUnary(mw, _ decorate.OpMiddleware) grpc.UnaryServerInterceptor {
	return unary(tier(mw))
}

// adaptStream runs each streaming RPC through the stream middleware (no timeout).
func adaptStream(_, mw decorate.OpMiddleware) grpc.StreamServerInterceptor {
	return stream(tier(mw))
}

// tier runs an RPC through mw as the operation named by its method. The handler's own
// error is returned unchanged; one the tier produced is sanitized (statusError).
func tier(mw decorate.OpMiddleware) around {
	return func(
		ctx context.Context, fullMethod string, next func(context.Context) error,
	) error {
		var handlerErr error
		err := mw.WrapOp(
			ctx,
			strings.TrimPrefix(fullMethod, "/"),
			func(ctx context.Context) error {
				handlerErr = next(ctx)
				return handlerErr
			},
		)
		return statusError(ctx, err, handlerErr)
	}
}

// statusError returns the handler's own error unchanged, and an error the tier produced
// (or rewrapped) as a sanitized status of its code; the real error is captured for the
// request log (core/errors.Capture).
func statusError(ctx context.Context, err, handlerErr error) error {
	if err == nil ||
		err == handlerErr { //nolint:errorlint // identity: the handler's own error
		return err
	}
	apperr.Capture(ctx, err)
	return wire.SanitizeGRPC(err)
}
