// Package decorators wraps the outbox sinks' SDK seams in the client stack
// (Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging)
// and codes every SDK error so the stack retries only transient failures.
package decorators

import (
	"context"
	"io"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	smithy "github.com/aws/smithy-go"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// SQSAPI runs every SQS call through stack. Both calls are retryable: a queue
// lookup is a read, and a re-sent batch only risks a duplicate the outbox's
// at-least-once contract already allows.
func SQSAPI(inner outboxsqs.API, stack *clientdecorators.Stack) outboxsqs.API {
	return &sqsAPI{inner: inner, stack: stack}
}

// sqsAPI is the decorated SQS seam.
type sqsAPI struct {
	// inner is the wrapped client.
	inner outboxsqs.API
	// stack applies the client-boundary layers.
	stack *clientdecorators.Stack
}

// GetQueueUrl resolves the queue URL through the stack.
func (a *sqsAPI) GetQueueUrl(
	ctx context.Context, in *awssqs.GetQueueUrlInput, optFns ...func(*awssqs.Options),
) (*awssqs.GetQueueUrlOutput, error) {
	return clientdecorators.Run(
		ctx,
		a.stack,
		"sqs.get_queue_url",
		clientdecorators.RunOpts{Retryable: true},
		func(cctx context.Context) (*awssqs.GetQueueUrlOutput, error) {
			out, err := a.inner.GetQueueUrl(cctx, in, optFns...)
			return out, classify(err)
		},
	)
}

// SendMessageBatch sends one batch through the stack.
func (a *sqsAPI) SendMessageBatch(
	ctx context.Context,
	in *awssqs.SendMessageBatchInput,
	optFns ...func(*awssqs.Options),
) (*awssqs.SendMessageBatchOutput, error) {
	return clientdecorators.Run(
		ctx,
		a.stack,
		"sqs.send_message_batch",
		clientdecorators.RunOpts{Retryable: true},
		func(cctx context.Context) (*awssqs.SendMessageBatchOutput, error) {
			out, err := a.inner.SendMessageBatch(cctx, in, optFns...)
			return out, classify(err)
		},
	)
}

// S3API runs every PutObject through stack. A put is retried only when its body
// can be rewound, and it is rewound before every attempt so a retry writes the
// whole payload.
func S3API(inner outboxs3.API, stack *clientdecorators.Stack) outboxs3.API {
	return &s3API{inner: inner, stack: stack}
}

// s3API is the decorated S3 seam.
type s3API struct {
	// inner is the wrapped client.
	inner outboxs3.API
	// stack applies the client-boundary layers.
	stack *clientdecorators.Stack
}

// PutObject writes one object through the stack.
func (a *s3API) PutObject(
	ctx context.Context, in *awss3.PutObjectInput, optFns ...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	seeker, rewindable := in.Body.(io.Seeker)
	return clientdecorators.Run(
		ctx,
		a.stack,
		"s3.put_object",
		clientdecorators.RunOpts{Retryable: rewindable},
		func(cctx context.Context) (*awss3.PutObjectOutput, error) {
			if rewindable {
				if _, err := seeker.Seek(0, io.SeekStart); err != nil {
					return nil, apperr.Wrap(
						err,
						apperr.CodeInternal,
						"outbox s3: rewind body",
					)
				}
			}
			out, err := a.inner.PutObject(cctx, in, optFns...)
			return out, classify(err)
		},
	)
}

// classify codes an SDK error: a throttle or server fault is CodeUnavailable
// (retryable), access denial CodeForbidden, any other client fault
// CodeInvalidInput; context errors and already-coded errors pass through.
func classify(err error) error {
	if err == nil || apperr.Code(err) != apperr.CodeUnknown ||
		apperr.StdIs(
			err,
			context.Canceled,
		) || apperr.StdIs(err, context.DeadlineExceeded) {
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
