package outbox

import (
	"context"
	"io"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	smithy "github.com/aws/smithy-go"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// decorateSQS runs every SQS call through the client tier and codes its errors, so
// only transient failures are retried. Both calls are retryable: a queue
// lookup is a read, and a re-sent batch only risks a duplicate the outbox's
// at-least-once contract already allows.
func decorateSQS(inner outboxsqs.API, c client.Base) outboxsqs.API {
	return &sqsAPI{inner: inner, Base: c}
}

// sqsAPI is the decorated SQS seam.
type sqsAPI struct {
	// inner is the wrapped client.
	inner outboxsqs.API
	// Base runs the calls through the client tier.
	client.Base
}

// GetQueueUrl resolves the queue URL through the client tier.
func (a *sqsAPI) GetQueueUrl(
	ctx context.Context, in *awssqs.GetQueueUrlInput, optFns ...func(*awssqs.Options),
) (*awssqs.GetQueueUrlOutput, error) {
	return a.Exec(
		ctx,
		"get_queue_url",
		func(cctx context.Context) (*awssqs.GetQueueUrlOutput, error) {
			out, err := a.inner.GetQueueUrl(cctx, in, optFns...)
			return out, classify(err)
		},
	)
}

// SendMessageBatch sends one batch through the client tier.
func (a *sqsAPI) SendMessageBatch(
	ctx context.Context,
	in *awssqs.SendMessageBatchInput,
	optFns ...func(*awssqs.Options),
) (*awssqs.SendMessageBatchOutput, error) {
	return a.Exec(
		ctx,
		"send_message_batch",
		func(cctx context.Context) (*awssqs.SendMessageBatchOutput, error) {
			out, err := a.inner.SendMessageBatch(cctx, in, optFns...)
			return out, classify(err)
		},
	)
}

// decorateS3 runs every PutObject through the client tier and codes its errors. A put
// is retried only when its body can be rewound, and it is rewound before every
// attempt so a retry writes the whole payload.
func decorateS3(inner outboxs3.API, c client.Base) outboxs3.API {
	return &s3API{inner: inner, Base: c}
}

// s3API is the decorated S3 seam.
type s3API struct {
	// inner is the wrapped client.
	inner outboxs3.API
	// Base runs the calls through the client tier.
	client.Base
}

// PutObject writes one object through the client tier.
func (a *s3API) PutObject(
	ctx context.Context, in *awss3.PutObjectInput, optFns ...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	seeker, rewindable := in.Body.(io.Seeker)
	put := func(ctx context.Context) (*awss3.PutObjectOutput, error) {
		if rewindable {
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return nil, apperr.Wrap(
					err,
					apperr.CodeInternal,
					"outbox s3: rewind body",
				)
			}
		}
		out, err := a.inner.PutObject(ctx, in, optFns...)
		return out, classify(err)
	}
	if rewindable {
		return a.Exec(ctx, "put_object", put)
	}
	return a.ExecOnce(ctx, "put_object", put)
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
