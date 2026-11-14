package interceptors

import (
	"context"
	"reflect"

	"connectrpc.com/connect/v2"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	coreprincipal "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/principal"
)

// errPrincipalResolution is the client-facing message for every failed principal
// resolution; the Connect code carries the classification and the real cause goes
// to the request's error sink, so resolver text (often a database error) never
// reaches the client.
const errPrincipalResolution = "principal resolution failed"

// PrincipalResolver resolves an authenticated caller's claims into the consumer's
// principal type P — its own authorization model (roles, memberships, grants). An
// error should carry a core/apperr code (or be a *connect.Error); a nil principal
// with a nil error rejects the caller as unauthenticated.
type PrincipalResolver[P any] func(ctx context.Context, claims *AuthClaims) (P, error)

// principalInterceptor resolves the request's AuthClaims into a principal of type P
// for every RPC shape. It must run after the auth interceptor so the claims are present.
// Streaming coverage matters because a unary-only resolver would leave server-streaming
// handlers without an authorization context.
type principalInterceptor[P any] struct {
	// resolve maps claims to the consumer's principal.
	resolve PrincipalResolver[P]
	// stub, when non-nil, builds the principal for synthetic (local-dev) claims
	// without calling resolve.
	stub func(claims *AuthClaims) P
}

// NewPrincipalInterceptor returns a server interceptor that resolves each authenticated
// request's claims into the consumer's principal type P and stores it with
// core/principal.WithPrincipal (read it with core/principal.From). Per RPC:
//   - no AuthClaims (unauthenticated/public request) → pass through
//   - synthetic claims and a non-nil stub → the stub principal, no resolve call; real
//     claims always resolve, so a caller behind the gateway never gets the stub identity
//   - otherwise → resolve; an error fails the request CLOSED. A *connect.Error keeps its
//     code, a core/apperr code maps to its Connect code (FORBIDDEN → PermissionDenied,
//     UNAUTHORIZED → Unauthenticated, NOT_FOUND → NotFound, …), and an uncoded error
//     becomes Unavailable, so a transient outage stays retryable. The client sees only a
//     generic message; the real error is captured for the request log
//     (core/apperr.Capture).
//   - a nil principal (from resolve or the stub) → CodeUnauthenticated
//
// The raw AuthClaims are cleared from the context handed downstream (GetAuthClaims then
// reports none), so handlers read the resolved principal rather than unresolved claims.
// It panics if resolve is nil, so a wiring mistake fails at startup, not per request.
func NewPrincipalInterceptor[P any](
	resolve PrincipalResolver[P],
	stub func(claims *AuthClaims) P,
) connect.ServerInterceptor {
	if resolve == nil {
		panic("interceptors: NewPrincipalInterceptor requires a non-nil resolver")
	}

	resolver := principalInterceptor[P]{resolve: resolve, stub: stub}

	return contextInterceptor(
		func(ctx context.Context, _ connect.Spec, _ *connect.Header) (
			context.Context, error,
		) {
			return resolver.withPrincipal(ctx)
		},
	)
}

// withPrincipal resolves the caller's principal and returns a context carrying it,
// with the raw AuthClaims cleared.
func (i principalInterceptor[P]) withPrincipal(
	ctx context.Context,
) (context.Context, error) {
	claims, ok := GetAuthClaims(ctx)

	ctx = WithAuthClaims(ctx, nil) // Clear raw claims so none leak downstream unresolved.

	if !ok {
		return ctx, nil
	}

	var principal P

	if i.stub != nil && claims.Synthetic {
		principal = i.stub(claims)
	} else {
		resolved, err := i.resolve(ctx, claims)
		if err != nil {
			return ctx, resolveError(ctx, err, resolveErrorCode(err))
		}

		principal = resolved
	}

	if isNilPrincipal(principal) {
		cause := apperr.Unauthorized("principal resolver returned a nil principal")

		return ctx, resolveError(ctx, cause, connect.CodeUnauthenticated)
	}

	return coreprincipal.WithPrincipal(ctx, principal), nil
}

// resolveError captures err as the request's real error (for the access log) and
// returns the sanitized client-facing Connect error carrying code.
func resolveError(ctx context.Context, err error, code connect.Code) error {
	apperr.Capture(ctx, err)

	return connect.NewError(code, errPrincipalResolution)
}

// resolveErrorCode classifies a resolver error from its code, never its message: a
// *connect.Error keeps its code, a core/apperr code maps to the matching Connect code,
// and an uncoded error is Unavailable (treated as transient).
func resolveErrorCode(err error) connect.Code {
	var connectErr *connect.Error

	if apperr.As(err, &connectErr) {
		return connectErr.Code()
	}

	code := apperr.Code(err)
	if mapped, ok := callerFaultCode(code); ok {
		return mapped
	}

	if code == apperr.CodeUnknown || code == apperr.CodeUnavailable ||
		code == apperr.CodeUpstream {
		return connect.CodeUnavailable
	}

	return serverFaultCode(code)
}

// serverFaultCode maps the remaining application codes.
func serverFaultCode(code apperr.ErrorCode) connect.Code {
	if code == apperr.CodeTimeout {
		return connect.CodeDeadlineExceeded
	}

	if code == apperr.CodeCanceled {
		return connect.CodeCanceled
	}

	if code == apperr.CodeResourceExhausted {
		return connect.CodeResourceExhausted
	}

	return connect.CodeInternal
}

// callerFaultCode maps the application codes that blame the caller.
func callerFaultCode(code apperr.ErrorCode) (connect.Code, bool) {
	if code == apperr.CodeNotFound {
		return connect.CodeNotFound, true
	}

	if code == apperr.CodeInvalidInput {
		return connect.CodeInvalidArgument, true
	}

	if code == apperr.CodeConflict {
		return connect.CodeAlreadyExists, true
	}

	if code == apperr.CodeUnauthorized {
		return connect.CodeUnauthenticated, true
	}

	if code == apperr.CodeForbidden {
		return connect.CodePermissionDenied, true
	}

	return 0, false
}

// isNilPrincipal reports whether p is a nil value of a nillable kind (pointer,
// interface, map, slice, func, chan), which must never be stored as a principal.
func isNilPrincipal[P any](p P) bool {
	v := reflect.ValueOf(&p).Elem()
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice,
		reflect.Func, reflect.Chan:
		return v.IsNil()
	default:
		return false
	}
}
