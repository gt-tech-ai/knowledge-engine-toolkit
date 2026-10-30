// Package wire is the codec between RPC status codes (Connect and gRPC share one code
// space) and the domain's apperr codes. A decorated RPC client codes each framework
// error with FromError before its client tier sees it, so retry and the breaker classify
// an RPC failure by the same policy as every other client's, and turns the tier's result
// back into its framework's error with ToGRPC or ToConnect. transport/rpc maps the
// server side's errors through the same table.
package wire

import (
	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// mapping is the domain code and fixed message a wire status maps back to.
type mapping struct {
	// msg is the fixed, client-safe message attached to the wrapped error.
	msg string
	// code is the domain ErrorCode for the wire status.
	code apperr.ErrorCode
}

// fromWire maps each recognized wire status to its domain code — the inverse of Code.
// Unimplemented is the caller's error (a method the server lacks), so it is invalid
// input, never retried. An unrecognized status maps to CodeUnknown.
func fromWire(c connect.Code) mapping {
	switch c {
	case connect.CodeNotFound:
		return mapping{code: apperr.CodeNotFound, msg: "resource not found"}
	case connect.CodeInvalidArgument:
		return mapping{code: apperr.CodeInvalidInput, msg: "invalid input"}
	case connect.CodeUnimplemented:
		return mapping{code: apperr.CodeInvalidInput, msg: "unimplemented"}
	case connect.CodeAlreadyExists:
		return mapping{code: apperr.CodeConflict, msg: "resource already exists"}
	case connect.CodeUnauthenticated:
		return mapping{code: apperr.CodeUnauthorized, msg: "unauthenticated"}
	case connect.CodePermissionDenied:
		return mapping{code: apperr.CodeForbidden, msg: "permission denied"}
	case connect.CodeUnavailable:
		return mapping{code: apperr.CodeUpstream, msg: "upstream service unavailable"}
	case connect.CodeDeadlineExceeded:
		return mapping{code: apperr.CodeTimeout, msg: "deadline exceeded"}
	case connect.CodeCanceled:
		return mapping{code: apperr.CodeCanceled, msg: "request canceled"}
	case connect.CodeResourceExhausted:
		return mapping{code: apperr.CodeResourceExhausted, msg: "resource exhausted"}
	case connect.CodeInternal:
		return mapping{code: apperr.CodeInternal, msg: "internal server error"}
	default:
		return mapping{code: apperr.CodeUnknown, msg: "unknown error"}
	}
}

// FromError codes a Connect or gRPC status error with its domain code, keeping the
// original in the chain (ToGRPC and ToConnect return it unchanged). Nil returns nil;
// an error that already carries a code is returned unchanged. Connect codes are
// preferred; otherwise a gRPC status is read (connect.CodeOf does not unwrap
// status.Error). An unrecognized code or a plain error is CodeUnknown.
func FromError(err error) error {
	if err == nil {
		return nil
	}
	if apperr.Code(err) != apperr.CodeUnknown {
		return err
	}
	c := connect.CodeOf(err)
	if c == connect.CodeUnknown {
		if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
			c = connect.Code(st.Code())
		}
	}
	m := fromWire(c)
	return apperr.Wrap(err, m.code, m.msg)
}

// Code maps a domain code to its wire status — the inverse of FromError's table.
func Code(code apperr.ErrorCode) connect.Code {
	return connect.Code(apperr.GRPCStatus(code))
}

// grpcStatus is a gRPC status error.
type grpcStatus interface {
	error

	// GRPCStatus returns the error's status.
	GRPCStatus() *status.Status
}

// ToGRPC returns err as a gRPC status error: the status the server sent when the chain
// carries one (so a caller sees the call's own failure verbatim), else a status of err's
// domain code (an open breaker, a full bulkhead, a client-tier timeout). Nil returns nil.
func ToGRPC(err error) error {
	if err == nil {
		return nil
	}
	var sent grpcStatus
	if apperr.As(err, &sent) {
		return sent
	}
	return status.Error(codes.Code(Code(apperr.Code(err))), err.Error())
}

// ToConnect returns err as a Connect error: the *connect.Error the server sent when the
// chain carries one, else one of err's domain code. Nil returns nil.
func ToConnect(err error) error {
	if err == nil {
		return nil
	}
	var cerr *connect.Error
	if apperr.As(err, &cerr) {
		return cerr
	}
	return connect.NewError(Code(apperr.Code(err)), err)
}
