package sqs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/lifecycle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
)

// maxBatchSize is the SQS per-request cap for SendMessageBatch and
// DeleteMessageBatch (the AWS limit is 10 entries).
const maxBatchSize = 10

// Compile-time interface assertion.
var _ interfaces.MessagePublisher = (*Publisher)(nil)

// Publisher publishes messages to SQS via the AWS SDK. Each topic is an SQS
// queue name; its URL is resolved once (creating the queue if absent) and
// cached for the publisher's lifetime.
type Publisher struct {
	// NoOp supplies the no-op Start/Stop: the SQS client is stateless (SendMessage is an
	// independent request), so there is no connection to open or close.
	lifecycle.NoOp

	// api is the SQS client (real, or an injected mock in tests).
	api API

	// urls caches topic (queue name) → resolved queue URL.
	urls map[string]string

	// mu guards urls.
	mu sync.Mutex
}

// NewPublisher creates an SQS message publisher. It uses Config.API when set,
// otherwise builds the real AWS SDK client from cfg (endpoint + credentials), loading
// the AWS config (credential chain, region) under ctx so a caller's deadline or
// cancellation bounds client construction.
func NewPublisher(ctx context.Context, cfg Config) (*Publisher, error) {
	api, err := resolveAPI(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Publisher{api: api, urls: make(map[string]string)}, nil
}

// Publish sends payload to the queue named topic, resolving (and creating, if
// absent) the queue URL on first use.
func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error {
	url, err := p.queueURL(ctx, topic)
	if err != nil {
		return err
	}
	if _, err := p.api.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:          aws.String(url),
		MessageBody:       aws.String(string(payload)),
		MessageAttributes: traceAttributes(ctx),
	}); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "sqs send message")
	}
	return nil
}

// PublishBatch sends payloads to topic using SendMessageBatch, chunked into
// groups of maxBatchSize (so N payloads issue ceil(N/10) requests, not N). A
// batch-level transport error, or any per-entry failure the service reports in
// SendMessageBatchOutput.Failed, is surfaced as an error naming the failed
// entries — a partial failure is not silently dropped.
func (p *Publisher) PublishBatch(
	ctx context.Context,
	topic string,
	payloads [][]byte,
) error {
	if len(payloads) == 0 {
		return nil
	}
	url, err := p.queueURL(ctx, topic)
	if err != nil {
		return err
	}
	attrs := traceAttributes(ctx)
	for start := 0; start < len(payloads); start += maxBatchSize {
		end := min(start+maxBatchSize, len(payloads))
		entries := make([]sqstypes.SendMessageBatchRequestEntry, 0, end-start)
		for i := start; i < end; i++ {
			entries = append(entries, sqstypes.SendMessageBatchRequestEntry{
				// Id is the per-request entry id (must be unique within the batch);
				// the payload index serves that role.
				Id:                aws.String(strconv.Itoa(i)),
				MessageBody:       aws.String(string(payloads[i])),
				MessageAttributes: attrs,
			})
		}
		out, err := p.api.SendMessageBatch(ctx, &awssqs.SendMessageBatchInput{
			QueueUrl: aws.String(url),
			Entries:  entries,
		})
		if err != nil {
			return apperr.Wrap(err, apperr.CodeUnavailable, "sqs send message batch")
		}
		if len(out.Failed) > 0 {
			return failedEntriesError("sqs send message batch", out.Failed)
		}
	}
	return nil
}

// traceAttributes returns ctx's propagated trace context as string message attributes
// (traceparent, tracestate, baggage), so the subscriber continues the publisher's trace;
// nil when ctx carries none.
func traceAttributes(ctx context.Context) map[string]sqstypes.MessageAttributeValue {
	carrier := map[string]string{}
	tracer.Inject(ctx, carrier)
	if len(carrier) == 0 {
		return nil
	}
	attrs := make(map[string]sqstypes.MessageAttributeValue, len(carrier))
	for k, v := range carrier {
		attrs[k] = sqstypes.MessageAttributeValue{
			DataType:    aws.String("String"),
			StringValue: aws.String(v),
		}
	}
	return attrs
}

// failedEntriesError builds an error summarizing the failed entries of a batch
// SQS response (SendMessageBatch / DeleteMessageBatch), naming each entry's id
// and service error code so a partial failure is actionable.
func failedEntriesError(op string, failed []sqstypes.BatchResultErrorEntry) error {
	parts := make([]string, len(failed))
	for i, f := range failed {
		parts[i] = aws.ToString(f.Id) + ":" + aws.ToString(f.Code)
	}
	return apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
		"%s: failed entries [%s]", op, strings.Join(parts, ", "),
	))
}

// queueURL returns the cached URL for topic, resolving it via ensureQueue on
// first use.
func (p *Publisher) queueURL(ctx context.Context, topic string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if url, ok := p.urls[topic]; ok {
		return url, nil
	}
	url, err := ensureQueue(ctx, p.api, topic)
	if err != nil {
		return "", err
	}
	p.urls[topic] = url
	return url, nil
}
