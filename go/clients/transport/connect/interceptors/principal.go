package interceptors

import (
	"context"
	"reflect"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errctx"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	coreprincipal "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/principal"
)

// errPrincipalResolution is the client-facing message for every failed principal
// resolution; the Connect code carries the classification and the real cause goes
// to the request's error sink, so resolver text (often a database error) never
// reaches the client.
const errPrincipalResolution = "principal resolution failed"

// PrincipalResolver resolves an authenticated caller's claims into the consumer's
// principal type P — its own authorization model (roles, memberships, grants). An
// error should carry a core/errors code (or be a *connect.Error); a nil principal
// with a nil error rejects the caller as unauthenticated.
type PrincipalResolver[P any] func(ctx context.Context, claims *AuthClaims) (P, error)

// principalInterceptor resolves the request's AuthClaims into a principal of type P
// for BOTH unary and streaming RPCs. It must run after the auth interceptor so the
// claims are present. Streaming coverage matters because a unary-only resolver would
// leave server-streaming handlers without an authorization context.
type principalInterceptor[P any] struct {
	// resolve maps claims to the consumer's principal.
	resolve PrincipalResolver[P]
	// stub, when non-nil, builds the principal for synthetic (local-dev) claims
	// without calling resolve.
	stub func(claims *AuthClaims) P
}

// NewPrincipalInterceptor returns an interceptor that resolves each authenticated
// request's claims into the consumer's principal type P and stores it with
// core/principal.WithPrincipal (read it with core/principal.PrincipalFrom). Per RPC:
//   - no AuthClaims (unauthenticated/public request) → pass through
//   - synthetic claims and a non-nil stub → the stub principal, no resolve call; real
//     claims always resolve, so a caller behind the gateway never gets the stub identity
//   - otherwise → resolve; an error fails the request CLOSED. A *connect.Error keeps its
//     code, a core/errors code maps to its Connect code (FORBIDDEN → PermissionDenied,
//     UNAUTHORIZED → Unauthenticated, NOT_FOUND → NotFound, …), and an uncoded error
//     becomes Unavailable, so a transient outage stays retryable. The client sees only a
//     generic message; the real error is captured for the request log (core/errctx).
//   - a nil principal (from resolve or the stub) → CodeUnauthenticated
//
// The raw AuthClaims are cleared from the context handed downstream (GetAuthClaims then
// reports none), so handlers read the resolved principal rather than unresolved claims.
// It panics if resolve is nil, so a wiring mistake fails at startup, not per request.
func NewPrincipalInterceptor[P any](
	resolve PrincipalResolver[P],
	stub func(claims *AuthClaims) P,
) connect.Interceptor {
	if resolve == nil {
		panic("interceptors: NewPrincipalInterceptor requires a non-nil resolver")
	}
	return principalInterceptor[P]{resolve: resolve, stub: stub}
}

// WrapUnary resolves the unary request's principal, failing closed on a resolver error.
func (i principalInterceptor[P]) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resolvedCtx, err := i.withPrincipal(ctx)
		if err != nil {
			return nil, err
		}
		return next(resolvedCtx, req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side resolution interceptor.
func (i principalInterceptor[P]) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler resolves the streaming request's principal, failing closed on a
// resolver error so a streaming call cannot proceed without an authorization context.
func (i principalInterceptor[P]) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		resolvedCtx, err := i.withPrincipal(ctx)
		if err != nil {
			return err
		}
		return next(resolvedCtx, conn)
	}
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
	errctx.Capture(ctx, err)
	return connect.NewError(code, apperr.Sentinel(errPrincipalResolution))
}

// resolveErrorCode classifies a resolver error from its code, never its message: a
// *connect.Error keeps its code, a core/errors code maps to the matching Connect code,
// and an uncoded error is Unavailable (treated as transient).
func resolveErrorCode(err error) connect.Code {
	var connectErr *connect.Error
	if apperr.As(err, &connectErr) {
		return connectErr.Code()
	}
	switch apperr.Code(err) {
	case apperr.CodeUnknown, apperr.CodeUnavailable, apperr.CodeUpstream:
		return connect.CodeUnavailable
	case apperr.CodeNotFound:
		return connect.CodeNotFound
	case apperr.CodeInvalidInput:
		return connect.CodeInvalidArgument
	case apperr.CodeConflict:
		return connect.CodeAlreadyExists
	case apperr.CodeUnauthorized:
		return connect.CodeUnauthenticated
	case apperr.CodeForbidden:
		return connect.CodePermissionDenied
	case apperr.CodeTimeout:
		return connect.CodeDeadlineExceeded
	case apperr.CodeCanceled:
		return connect.CodeCanceled
	case apperr.CodeResourceExhausted:
		return connect.CodeResourceExhausted
	case apperr.CodeInternal,
		apperr.CodeIngestion,
		apperr.CodeQualityFailed:
		return connect.CodeInternal
	default:
		return connect.CodeInternal
	}
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
