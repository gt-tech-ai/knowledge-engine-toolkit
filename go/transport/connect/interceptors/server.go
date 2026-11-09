package interceptors

import (
	"context"
	"strings"

	"connectrpc.com/connect"

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
	Authn []connect.Interceptor

	// Inner run after the tier, nearest the handler: the per-request retry budget
	// (BudgetInterceptor), request validation (ValidateInterceptor).
	Inner []connect.Interceptor
}

// ServerOptions returns the handler options that run every RPC of a generated Connect
// handler through the controller tier as the controller named name over d (see
// transport.Interceptors), so its layers are switched and tuned by the decoration
// config's controllers tier:
//
//   - Tracing continues the caller's trace in a server span and returns the
//     traceresponse header (TracingInterceptor), metrics count requests by status code
//     (MetricsInterceptor), and logging writes a status-aware access line
//     (NewLoggingInterceptor).
//   - Recovery, the rate limit, the shedding bulkhead, auth and the timeout run the
//     tier's own middleware, the operation named by the procedure
//     ("pkg.Service/Method"); a stream gets no timeout.
//   - edge.Authn run just before the auth layer, edge.Inner after the tier.
//
// A handler's error reaches the caller unchanged; a rejection the tier produced itself
// (a panic, a shed request, a denial, a timeout) becomes a *connect.Error of its code.
// d must not be nil.
func ServerOptions(name string, d *decorate.Deps, edge Edge) []connect.HandlerOption {
	chain := transport.Interceptors(name, d, native(d), adapt, edge.Authn)

	chain = append(chain, edge.Inner...)

	if len(chain) == 0 {
		return nil
	}

	return []connect.HandlerOption{connect.WithInterceptors(chain...)}
}

// native returns the Connect interceptors standing in for the tier's observability
// layers.
func native(dRef *decorate.Deps) func(decorate.Layer) ([]connect.Interceptor, bool) {
	deps := *dRef

	return func(layer decorate.Layer) ([]connect.Interceptor, bool) {
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
func nativeTracing(dRef *decorate.Deps) []connect.Interceptor {
	d := *dRef

	return when(
		d.Tracer,
		func() connect.Interceptor { return TracingInterceptor(d.Tracer) },
	)
}

// nativeMetrics is the metrics interceptor, when a recorder is wired.
func nativeMetrics(dRef *decorate.Deps) []connect.Interceptor {
	d := *dRef

	return when(
		d.Metrics,
		func() connect.Interceptor { return MetricsInterceptor(d.Metrics) },
	)
}

// nativeLogging is the access-log interceptor, when a logger is wired.
func nativeLogging(dRef *decorate.Deps) []connect.Interceptor {
	d := *dRef

	return when(
		d.Logger,
		func() connect.Interceptor { return NewLoggingInterceptor(d.Logger) },
	)
}

// when returns the interceptor build makes, or none when its collaborator is unset.
func when[C comparable](
	collaborator C,
	build func() connect.Interceptor,
) []connect.Interceptor {
	var unset C

	if collaborator == unset {
		return nil
	}

	return []connect.Interceptor{build()}
}

// tierInterceptor runs a Connect handler's RPCs through a run of the tier's layers.
type tierInterceptor struct {
	// unary decorates a unary RPC.
	unary decorate.OpMiddleware

	// stream decorates a streaming RPC (no timeout).
	stream decorate.OpMiddleware
}

// adapt builds the interceptor over one run of the tier's layers.
func adapt(unary, stream decorate.OpMiddleware) connect.Interceptor {
	return tierInterceptor{unary: unary, stream: stream}
}

// WrapUnary runs a unary RPC through the run.
func (i tierInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		var (
			resp       connect.AnyResponse
			handlerErr error
		)

		err := i.unary.WrapOp(
			ctx,
			operation(req.Spec()),
			func(ctx context.Context) error {
				resp, handlerErr = next(ctx, req)

				return handlerErr
			},
		)

		return resp, connectError(ctx, err, handlerErr)
	}
}

// WrapStreamingClient is a no-op — this is a server-side interceptor.
func (tierInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler runs a streaming RPC through the run.
func (i tierInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		var handlerErr error

		err := i.stream.WrapOp(
			ctx,
			operation(conn.Spec()),
			func(ctx context.Context) error {
				handlerErr = next(ctx, conn)

				return handlerErr
			},
		)

		return connectError(ctx, err, handlerErr)
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
