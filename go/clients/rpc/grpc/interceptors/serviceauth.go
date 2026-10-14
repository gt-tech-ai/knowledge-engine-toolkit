package interceptors

import (
	"context"

	"google.golang.org/grpc/metadata"
)

// authorizationKey is the outgoing metadata key the service token rides on.
const authorizationKey = "authorization"

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
