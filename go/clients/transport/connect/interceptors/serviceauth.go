package interceptors

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// StaticTokenValidator validates a bearer token against a per-caller token map,
// satisfying the core interfaces.ServiceTokenValidator strategy.
// Each caller may carry a current and a previous token — a zero-downtime rotation
// window — and comparisons are constant-time to avoid timing oracles.
type StaticTokenValidator struct {
	// accepted maps a caller-service name to its accepted tokens (current + optional
	// previous). Empty tokens are never stored.
	accepted map[string][]string
}

// Ensure StaticTokenValidator satisfies the ServiceTokenValidator contract.
var _ interfaces.ServiceTokenValidator = (*StaticTokenValidator)(nil)

// NewStaticTokenValidator builds a validator from a {caller -> token} map (typically
// auth.service_tokens). A value may carry a comma-separated {current,previous} pair to
// allow a rotation window; blank entries are ignored, and a caller left with no non-empty
// token is dropped (so it can never match).
func NewStaticTokenValidator(serviceTokens map[string]string) *StaticTokenValidator {
	accepted := make(map[string][]string, len(serviceTokens))
	for caller, raw := range serviceTokens {
		var toks []string
		for t := range strings.SplitSeq(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				toks = append(toks, t)
			}
		}
		if len(toks) > 0 {
			accepted[caller] = toks
		}
	}
	return &StaticTokenValidator{accepted: accepted}
}

// Validate returns the caller whose current-or-previous token matches, using a
// constant-time compare on each candidate. An empty token never matches.
func (v *StaticTokenValidator) Validate(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	for caller, toks := range v.accepted {
		for _, t := range toks {
			if subtle.ConstantTimeCompare([]byte(token), []byte(t)) == 1 {
				return caller, true
			}
		}
	}
	return "", false
}

const (
	// headerAuthorization carries the service-to-service bearer credential on internal RPCs.
	headerAuthorization = "Authorization"
	// bearerPrefix is the scheme prefix of an "Authorization: Bearer <token>" value.
	bearerPrefix = "Bearer "
	// stubCaller is the synthetic caller identity injected when service auth is stubbed
	// (dev).
	stubCaller = "stub-service"
)

// callerContextKey is the private context key under which the authenticated
// calling-service name is stored, keeping the slot collision-free across packages.
type callerContextKey struct{}

// WithCallerIdentity stores the authenticated calling-service name in the context.
func WithCallerIdentity(ctx context.Context, caller string) context.Context {
	return context.WithValue(ctx, callerContextKey{}, caller)
}

// GetCallerIdentity returns the calling-service name resolved by the service-auth
// interceptor, if the request was authenticated.
func GetCallerIdentity(ctx context.Context) (string, bool) {
	caller, ok := ctx.Value(callerContextKey{}).(string)
	return caller, ok
}

// serviceAuthInterceptor authenticates the CALLING SERVICE on the internal, non-gateway
// RPCs for BOTH unary and streaming handlers — Connect applies a
// connect.UnaryInterceptorFunc only to unary calls, so a unary-only guard would leave
// server-streaming handlers unauthenticated.
type serviceAuthInterceptor struct {
	// validator validates the caller service's bearer token.
	validator interfaces.ServiceTokenValidator
	// logger records auth outcomes (used by audit mode).
	logger interfaces.Logger
	// stub bypasses validation for local dev.
	stub bool
	// audit logs the outcome but admits the call (staged rollout) instead of rejecting.
	audit bool
}

// NewServiceAuthInterceptor returns an interceptor that validates the caller's
// `authorization: Bearer <token>` against the validator and attaches the resolved caller
// identity to the context; a missing/invalid credential is rejected with
// CodeUnauthenticated (fail-closed). When stub is true it bypasses validation for local
// dev (injecting a synthetic caller); when audit is true it logs the outcome but admits
// the call (deploy in audit, confirm every client sends a token, then flip to enforce).
func NewServiceAuthInterceptor(
	validator interfaces.ServiceTokenValidator,
	stub, audit bool,
	logger interfaces.Logger,
) connect.Interceptor {
	return serviceAuthInterceptor{
		validator: validator,
		stub:      stub,
		audit:     audit,
		logger:    logger,
	}
}

// WrapUnary authenticates the caller before the unary handler runs.
func (s serviceAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := s.authenticate(ctx, req.Header(), req.Spec().Procedure)
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op — this is a server-side authentication interceptor.
func (s serviceAuthInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler authenticates the caller before the streaming handler runs, so
// server-streaming RPCs are guarded identically to unary ones.
func (s serviceAuthInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := s.authenticate(ctx, conn.RequestHeader(), conn.Spec().Procedure)
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// authenticate resolves + validates the caller credential, returning a context carrying
// the caller identity, or a CodeUnauthenticated error. In stub mode it injects a
// synthetic caller and skips validation; in audit mode it logs the outcome but always
// admits.
func (s serviceAuthInterceptor) authenticate(
	ctx context.Context,
	h http.Header,
	procedure string,
) (context.Context, error) {
	if s.stub {
		return WithCallerIdentity(ctx, stubCaller), nil
	}
	caller, ok := s.validator.Validate(bearerToken(h.Get(headerAuthorization)))
	if !ok {
		if s.audit {
			if s.logger != nil {
				s.logger.Warn(
					"service auth: unauthenticated internal call admitted (audit mode)",
					"procedure", procedure,
				)
			}
			return ctx, nil
		}
		return nil, connect.NewError(
			connect.CodeUnauthenticated,
			apperr.Unauthorized("service authentication required"),
		)
	}
	if s.audit && s.logger != nil {
		s.logger.Info(
			"service auth: caller authenticated (audit mode)",
			"caller", caller,
			"procedure", procedure,
		)
	}
	return WithCallerIdentity(ctx, caller), nil
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header value,
// returning "" when the Bearer scheme is absent.
func bearerToken(header string) string {
	if len(header) >= len(bearerPrefix) &&
		strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
		return strings.TrimSpace(header[len(bearerPrefix):])
	}
	return ""
}
