package outbox

import (
	"context"
	"io"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/awserr"
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
			return out, awserr.Classify(err)
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
			return out, awserr.Classify(err)
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
		return out, awserr.Classify(err)
	}
	if rewindable {
		return a.Exec(ctx, "put_object", put)
	}
	return a.ExecOnce(ctx, "put_object", put)
}
