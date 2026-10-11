package interceptors

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// authorizationKey is the outgoing metadata key the service token rides on.
const authorizationKey = "authorization"

// ServiceAuthClientInterceptor returns a unary client interceptor that attaches the
// caller's service-to-service bearer token as `authorization: Bearer <token>` outgoing
// metadata, so the internal server's service-auth interceptor can authenticate this
// caller. An empty token attaches nothing — local dev leaves the token unset and the
// server's dev bypass admits the call.
//
// It never adds a second `authorization` value: if one is already on the outgoing
// context (set by the caller, or by another service-auth interceptor on the same
// connection — e.g. a conn built from both ClientBuilder and StreamingClientBuilder
// options), the call proceeds with that value alone, since `authorization` is not a
// list header and a server or proxy may reject two.
//
// The token travels as plain metadata; send it only over TLS (or a link a service
// mesh encrypts) — on the default insecure transport it crosses the network in
// cleartext.
func ServiceAuthClientInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		return invoker(withServiceToken(ctx, token), method, req, reply, cc, opts...)
	}
}

// ServiceAuthStreamClientInterceptor returns a stream client interceptor that attaches
// the caller's service-to-service bearer token as `authorization: Bearer <token>`
// outgoing metadata when a stream opens — the streaming counterpart of
// ServiceAuthClientInterceptor, with the same empty-token, single-value and
// transport-security rules.
func ServiceAuthStreamClientInterceptor(token string) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		return streamer(withServiceToken(ctx, token), desc, cc, method, opts...)
	}
}

// withServiceToken returns ctx with `authorization: Bearer <token>` appended to its
// outgoing metadata, or ctx unchanged when token is empty or an `authorization` value
// is already present.
func withServiceToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	md, _ := metadata.FromOutgoingContext(ctx) // a nil MD's Get returns nothing
	if len(md.Get(authorizationKey)) > 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, authorizationKey, "Bearer "+token)
}
