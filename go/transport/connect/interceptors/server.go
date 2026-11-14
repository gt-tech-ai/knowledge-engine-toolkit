package interceptors

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
)

// Edge is a Connect server's own interceptors around the controller tier.
type Edge struct {
	// Authn authenticate the caller, in order, just before the tier's auth layer:
	// service auth (NewServiceAuthInterceptor), gateway-header auth
	// (NewAuthInterceptor), then the consumer's principal (NewPrincipalInterceptor) and
	// tenant-scope (NewTenantScopeInterceptor) interceptors.
	Authn []connect.ServerInterceptor

	// Inner run after the tier, nearest the handler: the per-request retry budget
	// (BudgetInterceptor), request validation (ValidateInterceptor).
	Inner []connect.ServerInterceptor
}

// ServerInterceptors returns the interceptors that run every RPC registered on a
// *connect.Server through the controller tier as the controller named name over d (see
// transport.Interceptors), so its layers are switched and tuned by the decoration
// config's controllers tier. Pass them to connect.NewServer, which applies them to every
// service registered on it:
//
//   - Tracing continues the caller's trace in a server span and returns the
//     traceresponse header (TracingInterceptor), metrics count requests by status code
//     (MetricsInterceptor), and logging writes a status-aware access line
//     (NewLoggingInterceptor).
//   - Recovery, the rate limit, the shedding bulkhead, auth and the timeout run the
//     tier's own middleware, the operation named by the procedure
//     ("pkg.Service/Method"); a streaming RPC (any StreamType but unary) gets no
//     timeout.
//   - edge.Authn run just before the auth layer, edge.Inner after the tier.
//
// A handler's error reaches the caller unchanged; a rejection the tier produced itself
// (a panic, a shed request, a denial, a timeout) becomes a *connect.Error of its code.
// d must not be nil.
func ServerInterceptors(
	name string,
	d *decorate.Deps,
	edge Edge,
) []connect.ServerInterceptor {
	chain := transport.Interceptors(name, d, native(d), adapt, edge.Authn)

	return append(chain, edge.Inner...)
}

// native returns the Connect interceptors standing in for the tier's observability
// layers.
func native(
	dRef *decorate.Deps,
) func(decorate.Layer) ([]connect.ServerInterceptor, bool) {
	deps := *dRef

	return func(layer decorate.Layer) ([]connect.ServerInterceptor, bool) {
		switch layer {
		case decorate.LayerTracing:
			return nativeTracing(&deps), true
		case decorate.LayerMetrics:
			return nativeMetrics(&deps), true
		case decorate.LayerLogging:
			return nativeLogging(&deps), true
		case decorate.LayerRecovery,
			decorate.LayerAuth,
			decorate.LayerTimeout,
			decorate.LayerCircuitBreaker,
			decorate.LayerRetry,
			decorate.LayerBulkhead,
			decorate.LayerRateLimit:
			return nil, false
		default:
			// Any other value takes the shared result below.
		}

		return nil, false
	}
}

// nativeTracing is the tracing interceptor, when a tracer is wired.
func nativeTracing(dRef *decorate.Deps) []connect.ServerInterceptor {
	d := *dRef

	return when(
		d.Tracer,
		func() connect.ServerInterceptor { return TracingInterceptor(d.Tracer) },
	)
}

// nativeMetrics is the metrics interceptor, when a recorder is wired.
func nativeMetrics(dRef *decorate.Deps) []connect.ServerInterceptor {
	d := *dRef

	return when(
		d.Metrics,
		func() connect.ServerInterceptor { return MetricsInterceptor(d.Metrics) },
	)
}

// nativeLogging is the access-log interceptor, when a logger is wired.
func nativeLogging(dRef *decorate.Deps) []connect.ServerInterceptor {
	d := *dRef

	return when(
		d.Logger,
		func() connect.ServerInterceptor { return NewLoggingInterceptor(d.Logger) },
	)
}

// when returns the interceptor build makes, or none when its collaborator is unset.
func when[C comparable](
	collaborator C,
	build func() connect.ServerInterceptor,
) []connect.ServerInterceptor {
	var unset C

	if collaborator == unset {
		return nil
	}

	return []connect.ServerInterceptor{build()}
}

// adapt builds the interceptor over one run of the tier's layers: a unary RPC runs
// through unary, any streaming RPC through stream (no timeout). The handler's own error
// is returned unchanged; an error the run produced becomes a sanitized *connect.Error.
func adapt(unary, stream decorate.OpMiddleware) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(
			ctx context.Context,
			spec connect.Spec,
			serverStream connect.ServerStream,
		) error {
			run := stream
			if spec.StreamType == connect.StreamTypeUnary {
				run = unary
			}

			var handlerErr error

			err := run.WrapOp(
				ctx,
				operation(spec),
				func(ctx context.Context) error {
					handlerErr = next(ctx, spec, serverStream)

					return handlerErr
				},
			)

			return connectError(ctx, err, handlerErr)
		}
	}
}

// connectError returns the handler's own error unchanged, and an error the tier
// produced (or rewrapped) as a sanitized *connect.Error of its code; the real error is
// captured for the request log (core/apperr.Capture).
func connectError(ctx context.Context, err, handlerErr error) error {
	if err == nil || apperr.Identical(err, handlerErr) {
		return err
	}

	apperr.Capture(ctx, err)

	return wire.SanitizeConnect(err)
}

// operation names an RPC's operation by its procedure without the leading slash.
func operation(
	spec connect.Spec,
) string {
	return strings.TrimPrefix(spec.Procedure, "/")
}

// contextInterceptor is a server interceptor that derives the RPC's context from its
// request header before the handler runs, for every RPC shape alike; an error from
// derive rejects the RPC without calling the handler.
func contextInterceptor(
	derive func(ctx context.Context, spec connect.Spec, header *connect.Header) (
		context.Context, error,
	),
) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(
			ctx context.Context,
			spec connect.Spec,
			stream connect.ServerStream,
		) error {
			ctx, err := derive(ctx, spec, requestHeader(ctx))
			if err != nil {
				return err
			}

			return next(ctx, spec, stream)
		}
	}
}

// callInfo returns the RPC's server CallInfo, or an empty one when ctx did not come
// through connect.Server.Call (a ServerFunc invoked directly).
func callInfo(ctx context.Context) *connect.CallInfo {
	if info, ok := connect.CallInfoForServerContext(ctx); ok {
		return info
	}

	return new(connect.CallInfo)
}

// requestHeader returns the RPC's request header (empty outside connect.Server.Call).
func requestHeader(ctx context.Context) *connect.Header {
	return callInfo(ctx).RequestHeader()
}
