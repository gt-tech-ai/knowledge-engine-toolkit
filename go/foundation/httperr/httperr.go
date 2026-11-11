// Package httperr codes the error an HTTP client's send returns, so the client tier
// retries only what can recover. Every HTTP client family codes its sends through
// Transport.
package httperr

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// Transport codes a failed send with msg: a code err already carries is kept; the
// caller's deadline is CodeTimeout, its cancellation CodeCanceled, and anything else
// (a refused or reset connection) CodeUnavailable.
func Transport(err error, msg string) error {
	code := apperr.Code(err)
	switch {
	case code != apperr.CodeUnknown:
	case apperr.StdIs(err, context.DeadlineExceeded):
		code = apperr.CodeTimeout
	case apperr.StdIs(err, context.Canceled):
		code = apperr.CodeCanceled
	default:
		code = apperr.CodeUnavailable
	}

	return apperr.Wrap(err, code, msg)
}
