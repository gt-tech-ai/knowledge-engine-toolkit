package outbox

import (
	"context"
	"io"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	outboxs3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/s3"
	outboxsqs "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/outbox/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/awserr"
)

// decorateSQS runs every SQS call through the client tier and codes its errors, so
// only transient failures are retried. Both calls are retryable: a queue
// lookup is a read, and a re-sent batch only risks a duplicate the outbox's
// at-least-once contract already allows.
func decorateSQS(inner outboxsqs.API, cRef *client.Base) outboxsqs.Calls {
	c := *cRef

	api := &sqsAPI{inner: inner, Base: &c}

	return outboxsqs.Calls{
		GetQueueURL:      api.getQueueURL,
		SendMessageBatch: api.SendMessageBatch,
	}
}

// sqsAPI is the decorated SQS seam.
type sqsAPI struct {
	// Base runs the calls through the client tier.
	*client.Base

	// inner is the wrapped client.
	inner outboxsqs.API
}

// SendMessageBatch sends one batch through the client tier.
func (a *sqsAPI) SendMessageBatch(
	ctx context.Context,
	sendMessageBatchInput *awssqs.SendMessageBatchInput,
	optFns ...func(*awssqs.Options),
) (*awssqs.SendMessageBatchOutput, error) {
	return a.Exec(
		ctx,
		"send_message_batch",
		func(cctx context.Context) (*awssqs.SendMessageBatchOutput, error) {
			out, err := a.inner.SendMessageBatch(cctx, sendMessageBatchInput, optFns...)

			return out, awserr.Classify(err)
		},
	)
}

// getQueueURL resolves the queue URL through the client tier.
func (a *sqsAPI) getQueueURL(
	ctx context.Context,
	queueLookup *awssqs.GetQueueUrlInput,
	optFns ...func(*awssqs.Options),
) (*awssqs.GetQueueUrlOutput, error) {
	return a.Exec(
		ctx,
		"get_queue_url",
		func(cctx context.Context) (*awssqs.GetQueueUrlOutput, error) {
			out, err := a.inner.GetQueueUrl(cctx, queueLookup, optFns...)

			return out, awserr.Classify(err)
		},
	)
}

// decorateS3 runs every PutObject through the client tier and codes its errors. A put
// is retried only when its body can be rewound, and it is rewound before every
// attempt so a retry writes the whole payload.
func decorateS3(inner outboxs3.API, cRef *client.Base) outboxs3.API {
	c := *cRef

	return &s3API{inner: inner, Base: &c}
}

// s3API is the decorated S3 seam.
type s3API struct {
	// Base runs the calls through the client tier.
	*client.Base

	// inner is the wrapped client.
	inner outboxs3.API
}

// PutObject writes one object through the client tier.
func (a *s3API) PutObject(
	ctx context.Context,
	putObjectInput *awss3.PutObjectInput,
	optFns ...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	seeker, rewindable := putObjectInput.Body.(io.Seeker)
	put := func(ctx context.Context) (*awss3.PutObjectOutput, error) {
		if rewindable {
			_, err := seeker.Seek(0, io.SeekStart)
			if err != nil {
				return nil, apperr.Wrap(
					err,
					apperr.CodeInternal,
					"outbox s3: rewind body",
				)
			}
		}

		out, err := a.inner.PutObject(ctx, putObjectInput, optFns...)

		return out, awserr.Classify(err)
	}

	if rewindable {
		return a.Exec(ctx, "put_object", put)
	}

	return a.ExecOnce(ctx, "put_object", put)
}
