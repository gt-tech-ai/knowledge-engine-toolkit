// Package rpc provides Connect RPC utilities for bidirectional mapping between
// domain AppErrors and Connect/gRPC status codes.
package rpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errctx"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// msgInternal is the client-safe message every internal failure is reduced to.
const msgInternal = "internal server error"

// Sanitize maps a domain AppError to a client-safe Connect code and a generic
// message, stripping any implementation detail (stack traces, SQL, upstream
// provider text) that must never reach an API consumer.
//
// It performs the mapping only — it does not touch the request context or build
// a connect.Error. ToConnectError uses it for the RPC's top-level error, while
// handlers that must embed a sanitized failure in a response body (e.g. the
// per-invite failures of a batch invite) call it directly and remain
// responsible for recording the real cause server-side.
func Sanitize(err error) (code connect.Code, message string) {
	switch apperr.Code(err) {
	case apperr.CodeNotFound:
		return connect.CodeNotFound, "resource not found"
	case apperr.CodeInvalidInput:
		return connect.CodeInvalidArgument, "invalid input"
	case apperr.CodeConflict:
		return connect.CodeAlreadyExists, "resource already exists"
	case apperr.CodeUnauthorized:
		return connect.CodeUnauthenticated, "unauthenticated"
	case apperr.CodeForbidden:
		return connect.CodePermissionDenied, "permission denied"
	case apperr.CodeUpstream:
		return connect.CodeUnavailable, "upstream service unavailable"
	case apperr.CodeUnavailable:
		return connect.CodeUnavailable, "service unavailable"
	case apperr.CodeCanceled:
		return connect.CodeCanceled, "request canceled"
	case apperr.CodeTimeout:
		return connect.CodeDeadlineExceeded, "deadline exceeded"
	case apperr.CodeResourceExhausted:
		return connect.CodeResourceExhausted, "resource exhausted"
	case apperr.CodeUnknown,
		apperr.CodeInternal,
		apperr.CodeIngestion,
		apperr.CodeQualityFailed:
		return connect.CodeInternal, msgInternal
	default:
		return connect.CodeInternal, msgInternal
	}
}

// ToConnectError maps a domain AppError to a Connect error code and returns a
// client-safe error.
//
// The real error is captured into the request's error sink (errctx) so the
// request-logging interceptor can record the true cause once, while the value
// returned to the client carries only a sanitized message. This prevents
// leaking stack traces, SQL errors, or other implementation details to API
// consumers.
func ToConnectError(ctx context.Context, err error) error {
	// Record the real error for the request logger before sanitizing it.
	errctx.Capture(ctx, err)

	code, msg := Sanitize(err)
	return connect.NewError(code, apperr.Sentinel(msg))
}

// wireMapping is the domain code and fixed message a wire status maps back to.
type wireMapping struct {
	// code is the domain ErrorCode for the wire status.
	code apperr.ErrorCode
	// msg is the fixed, client-safe message attached to the wrapped error.
	msg string
}

// fromWire maps each recognized Connect/gRPC status to its domain code — the
// inverse of Sanitize. An unrecognized status maps to CodeUnknown.
func fromWire(wire connect.Code) wireMapping {
	switch wire {
	case connect.CodeNotFound:
		return wireMapping{apperr.CodeNotFound, "resource not found"}
	case connect.CodeInvalidArgument:
		return wireMapping{apperr.CodeInvalidInput, "invalid input"}
	case connect.CodeAlreadyExists:
		return wireMapping{apperr.CodeConflict, "resource already exists"}
	case connect.CodeUnauthenticated:
		return wireMapping{apperr.CodeUnauthorized, "unauthenticated"}
	case connect.CodePermissionDenied:
		return wireMapping{apperr.CodeForbidden, "permission denied"}
	case connect.CodeUnavailable:
		return wireMapping{apperr.CodeUpstream, "upstream service unavailable"}
	case connect.CodeDeadlineExceeded:
		return wireMapping{apperr.CodeTimeout, "deadline exceeded"}
	case connect.CodeCanceled:
		return wireMapping{apperr.CodeCanceled, "request canceled"}
	case connect.CodeResourceExhausted:
		return wireMapping{apperr.CodeResourceExhausted, "resource exhausted"}
	case connect.CodeInternal:
		return wireMapping{apperr.CodeInternal, msgInternal}
	default:
		return wireMapping{apperr.CodeUnknown, "unknown error"}
	}
}

// FromRPCError maps a Connect or gRPC status error to a domain AppError.
// Nil in returns nil. Errors that are already AppErrors are returned unchanged.
//
// Connect codes are preferred when present; otherwise a gRPC status is used
// (Connect's CodeOf does not unwrap status.Error). Unrecognized wire codes and
// plain errors map to CodeUnknown.
func FromRPCError(err error) error {
	if err == nil {
		return nil
	}
	if apperr.Code(err) != apperr.CodeUnknown {
		return err
	}

	wire := connect.CodeOf(err)
	if wire == connect.CodeUnknown {
		if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
			wire = connect.Code(st.Code())
		}
	}

	m := fromWire(wire)
	return apperr.Wrap(err, m.code, m.msg)
}
