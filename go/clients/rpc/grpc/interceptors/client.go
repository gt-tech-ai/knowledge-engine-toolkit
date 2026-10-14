// Package interceptors runs a gRPC client connection's calls through the client tier
// (client.Tier): ClientOptions installs it as dial options, carrying the trace context
// and the service token on every call.
package interceptors

import (
	"context"
	"strings"

	"google.golang.org/grpc"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
)

// ClientOptions returns the dial options that run every call on a connection through
// base, the client tier, so an RPC client is decorated like every other client — its
// layers switched and tuned by the clients tier of the decoration config:
//
//   - A unary call runs as the operation named by its method ("pkg.Service/Method"),
//     retried when the tier retries; each attempt gets the tier's deadline, span,
//     metrics and log line.
//   - A stream's open runs through base.Open: never retried, the tier's timeout bounding
//     only the open, so a long-lived stream outlives any call budget.
//
// Inside each attempt the active trace context is injected into the outgoing metadata
// (so the server continues this trace) and, when serviceToken is set, the
// service-to-service bearer token. The status error a server returns is coded for the
// tier (wire.FromError) and handed back to the caller unchanged; a failure the tier
// produced itself (an open breaker, a full bulkhead, a timeout) becomes the status of
// its code.
func ClientOptions(base client.Base, serviceToken string) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(unaryClient(base, serviceToken)),
		grpc.WithChainStreamInterceptor(streamClient(base, serviceToken)),
	}
}

// unaryClient runs each unary call through base.
func unaryClient(base client.Base, token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		return wire.ToGRPC(
			base.Do(ctx, operation(method), func(ctx context.Context) error {
				return wire.FromError(
					invoker(outgoing(ctx, token), method, req, reply, cc, opts...),
				)
			}),
		)
	}
}

// streamClient opens each stream through base.Open.
func streamClient(base client.Base, token string) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		stream, err := base.Open(ctx, operation(method), func(
			ctx context.Context,
		) (grpc.ClientStream, error) {
			s, err := streamer(outgoing(ctx, token), desc, cc, method, opts...)
			return s, wire.FromError(err)
		})
		if err != nil {
			return nil, wire.ToGRPC(err)
		}
		return stream, nil
	}
}

// outgoing returns ctx carrying the active trace context and the service token in its
// outgoing metadata.
func outgoing(ctx context.Context, token string) context.Context {
	return withServiceToken(injectTraceContext(ctx), token)
}

// operation names a call's operation by its method without the leading slash.
func operation(method string) string { return strings.TrimPrefix(method, "/") }
