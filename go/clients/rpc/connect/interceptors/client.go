// Package interceptors runs a generated Connect client's calls through the client tier
// (client.Tier): ClientOptions installs it as a Connect client interceptor.
package interceptors

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
)

// clientInterceptor runs a Connect client's calls through the client tier.
type clientInterceptor struct {
	// base is the client's decoration.
	base client.Base
}

// ClientOptions returns the client options that run every call of a generated Connect
// client through base, the client tier, so it is decorated like every other client —
// its layers switched and tuned by the clients tier of the decoration config. A unary
// call runs as the operation named by its procedure ("pkg.Service/Method"), retried
// when the tier retries and the schema declares the procedure idempotent (an
// idempotency_level of NO_SIDE_EFFECTS or IDEMPOTENT): a procedure of unknown
// idempotency is a write whose first attempt may have landed, so it runs at most once.
// Each attempt carries the active trace context in its
// headers. A streaming call only carries the trace context: Connect opens a stream
// without I/O, so there is no open to decorate. The *connect.Error a server returns is
// coded for the tier (wire.FromError) and handed back unchanged; a failure the tier
// produced itself becomes a *connect.Error of its code.
func ClientOptions(base client.Base) []connect.ClientOption {
	return []connect.ClientOption{connect.WithInterceptors(clientInterceptor{base: base})}
}

// WrapUnary runs a unary call through the tier.
func (i clientInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		op := strings.TrimPrefix(req.Spec().Procedure, "/")
		run := i.base.Exec[connect.AnyResponse]
		if req.Spec().IdempotencyLevel == connect.IdempotencyUnknown {
			run = i.base.ExecOnce[connect.AnyResponse]
		}
		resp, err := run(
			ctx,
			op,
			func(ctx context.Context) (connect.AnyResponse, error) {
				injectTrace(ctx, req.Header())
				resp, err := next(ctx, req)
				return resp, wire.FromError(err)
			},
		)
		if err != nil {
			return nil, wire.ToConnect(err)
		}
		return resp, nil
	}
}

// WrapStreamingClient carries the trace context on a stream's request headers.
func (i clientInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		injectTrace(ctx, conn.RequestHeader())
		return conn
	}
}

// WrapStreamingHandler is a no-op — this is a client-side interceptor.
func (i clientInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return next
}

// injectTrace writes ctx's W3C trace context into h, so the server continues this trace.
func injectTrace(ctx context.Context, h http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(h))
}
