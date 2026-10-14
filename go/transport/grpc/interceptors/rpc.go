package interceptors

import (
	"context"

	"google.golang.org/grpc"
)

// around runs one RPC, named by its full method, by calling next with the context the
// RPC continues in. Each server interceptor here is one around, so its unary and stream
// forms share a single body.
type around func(
	ctx context.Context, fullMethod string, next func(context.Context) error,
) error

// unary returns the unary server interceptor that runs each RPC through a.
func unary(a around) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		var resp any
		err := a(ctx, info.FullMethod, func(ctx context.Context) error {
			var err error
			resp, err = handler(ctx, req)
			return err
		})
		return resp, err
	}
}

// stream returns the stream server interceptor that runs each RPC through a; the
// handler sees the stream with the context a continues in.
func stream(a around) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		return a(ss.Context(), info.FullMethod, func(ctx context.Context) error {
			return handler(srv, contextStream{ServerStream: ss, ctx: ctx})
		})
	}
}

// contextStream is a server stream whose Context is the one an interceptor continued
// the RPC in.
type contextStream struct {
	grpc.ServerStream

	// ctx is the stream's context.
	ctx context.Context //nolint:containedctx // a ServerStream carries its context
}

// Context returns the stream's context.
func (s contextStream) Context() context.Context { return s.ctx }
