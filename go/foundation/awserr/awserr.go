// Package awserr codes the errors an AWS SDK v2 call returns, so the client tier
// retries only what can recover: a throttle or a server fault is CodeUnavailable, an
// access denial CodeForbidden and any other client fault CodeInvalidInput. Every AWS
// client family codes its calls through Classify.
package awserr

import (
	"context"
	"strings"

	smithy "github.com/aws/smithy-go"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// Classify codes an SDK error: a throttle or server fault is CodeUnavailable
// (retryable), access denial CodeForbidden, any other client fault
// CodeInvalidInput; nil, context errors and already-coded errors pass through.
func Classify(err error) error {
	if err == nil || apperr.Code(err) != apperr.CodeUnknown ||
		apperr.StdIs(err, context.Canceled) ||
		apperr.StdIs(err, context.DeadlineExceeded) {
		return err
	}

	var apiErr smithy.APIError

	if !apperr.As(err, &apiErr) || apiErr.ErrorFault() != smithy.FaultClient {
		return apperr.Wrap(err, apperr.CodeUnavailable, "aws call failed")
	}

	switch name := apiErr.ErrorCode(); {
	case strings.Contains(name, "Throttl"), name == "SlowDown", name == "RequestTimeout":
		return apperr.Wrap(err, apperr.CodeUnavailable, "aws call throttled")
	case strings.HasPrefix(name, "AccessDenied"):
		return apperr.Wrap(err, apperr.CodeForbidden, "aws call denied")
	default:
		return apperr.Wrap(err, apperr.CodeInvalidInput, "aws call rejected")
	}
}
