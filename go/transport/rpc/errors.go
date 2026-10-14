// Package rpc provides Connect RPC utilities for bidirectional mapping between
// domain AppErrors and Connect/gRPC status codes.
package rpc

import (
	"context"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/rpc/wire"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

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
	c := apperr.Code(err)
	return wire.Code(c), wire.SafeMessage(c)
}

// ToConnectError maps a domain AppError to a Connect error code and returns a
// client-safe error.
//
// The real error is captured into the request's error sink (core/errors.Capture) so the
// request-logging interceptor can record the true cause once, while the value
// returned to the client carries only a sanitized message. This prevents
// leaking stack traces, SQL errors, or other implementation details to API
// consumers.
func ToConnectError(ctx context.Context, err error) error {
	// Record the real error for the request logger before sanitizing it.
	apperr.Capture(ctx, err)

	code, msg := Sanitize(err)
	return connect.NewError(code, apperr.Sentinel(msg))
}

// FromRPCError maps a Connect or gRPC status error to a domain AppError, keeping the
// original in the chain (see wire.FromError, which owns the code table). Nil in returns
// nil; an error that already carries a code is returned unchanged; an unrecognized
// code or a plain error is CodeUnknown.
func FromRPCError(err error) error { return wire.FromError(err) }
