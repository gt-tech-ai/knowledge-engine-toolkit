package decorate

import (
	"context"
	"fmt"
)

// NewAuth returns a middleware that runs authFn with the action "<name>.<op>" before
// the operation and short-circuits (returns the deny error) when authFn fails, so a
// custom service operation is authorized exactly like the CRUD operations. A nil
// authFn is a no-op (the caller simply omits this middleware when there is no check).
func NewAuth(
	name string,
	authFn func(ctx context.Context, action string) error,
) OpMiddleware {
	return MiddlewareFunc(
		func(ctx context.Context, op string, next func(context.Context) error) error {
			if authFn != nil {
				if err := authFn(ctx, fmt.Sprintf("%s.%s", name, op)); err != nil {
					return err
				}
			}
			return next(ctx)
		},
	)
}
