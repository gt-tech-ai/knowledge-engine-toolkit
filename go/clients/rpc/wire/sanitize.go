package wire

import (
	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// msgInternal is the client-safe message every internal failure is reduced to.
const msgInternal = "internal server error"

// SafeMessage is the client-safe message for a domain code: a fixed text that carries
// no implementation detail (stack traces, SQL, upstream provider text, internal action
// names), so a server can send it to any caller.
func SafeMessage(code apperr.ErrorCode) string {
	switch code {
	case apperr.CodeNotFound:
		return "resource not found"
	case apperr.CodeInvalidInput:
		return "invalid input"
	case apperr.CodeConflict:
		return "resource already exists"
	case apperr.CodeUnauthorized:
		return "unauthenticated"
	case apperr.CodeForbidden:
		return "permission denied"
	case apperr.CodeUpstream:
		return "upstream service unavailable"
	case apperr.CodeUnavailable:
		return "service unavailable"
	case apperr.CodeCanceled:
		return "request canceled"
	case apperr.CodeTimeout:
		return "deadline exceeded"
	case apperr.CodeResourceExhausted:
		return "resource exhausted"
	case apperr.CodeUnknown,
		apperr.CodeInternal,
		apperr.CodeIngestion,
		apperr.CodeQualityFailed:
		return msgInternal
	default:
		return msgInternal
	}
}

// SanitizeConnect returns err as the *connect.Error a server sends: the *connect.Error
// the chain carries (a status a handler chose) verbatim, else one of err's domain code
// carrying only the code's SafeMessage. Nil returns nil.
func SanitizeConnect(err error) error {
	if err == nil {
		return nil
	}
	var cerr *connect.Error
	if apperr.As(err, &cerr) {
		return cerr
	}
	c := apperr.Code(err)
	return connect.NewError(Code(c), apperr.Sentinel(SafeMessage(c)))
}

// SanitizeGRPC returns err as the gRPC status error a server sends: the status the chain
// carries (one a handler chose) verbatim, else a status of err's domain code carrying
// only the code's SafeMessage. Nil returns nil.
func SanitizeGRPC(err error) error {
	if err == nil {
		return nil
	}
	var sent grpcStatus
	if apperr.As(err, &sent) {
		return sent
	}
	c := apperr.Code(err)
	return status.Error(codes.Code(Code(c)), SafeMessage(c))
}
