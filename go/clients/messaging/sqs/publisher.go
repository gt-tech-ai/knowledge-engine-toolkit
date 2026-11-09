package sqs

import (
	"context"
	"fmt"
	"maps"
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
var _ interfaces.KeyedPublisher = (*Publisher)(nil)

// Publisher publishes messages to SQS via the AWS SDK. Each topic is an SQS
// queue name; its URL is resolved once (creating the queue if absent) and
// cached for the publisher's lifetime. A keyed publish carries its key as the
// interfaces.PartitionKeyAttribute string attribute; a standard queue does not order by
// it, so OrderedPerKey is false.
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
	return p.send(ctx, topic, traceAttributes(ctx), payload)
}

// PublishKeyed sends payload to the queue named topic with key as its partition_key
// attribute.
func (p *Publisher) PublishKeyed(
	ctx context.Context,
	topic, key string,
	payload []byte,
) error {
	return p.send(ctx, topic, withKey(traceAttributes(ctx), key), payload)
}

// PublishBatchKeyed sends items to topic with SendMessageBatch, each entry carrying its
// key as the partition_key attribute; chunking and partial-failure handling are
// PublishBatch's.
func (p *Publisher) PublishBatchKeyed(
	ctx context.Context,
	topic string,
	items []interfaces.KeyedPayload,
) error {
	trace := traceAttributes(ctx)
	entries := make([]sqstypes.SendMessageBatchRequestEntry, len(items))

	for i, item := range items {
		entries[i] = sqstypes.SendMessageBatchRequestEntry{
			MessageBody:       aws.String(string(item.Payload)),
			MessageAttributes: withKey(trace, item.Key),
		}
	}

	return p.sendBatch(ctx, topic, entries)
}

// OrderedPerKey is false: a standard SQS queue does not order messages by key.
func (*Publisher) OrderedPerKey() bool { return false }

// send sends one message with attrs to the queue named topic.
func (p *Publisher) send(
	ctx context.Context,
	topic string,
	attrs map[string]sqstypes.MessageAttributeValue,
	payload []byte,
) error {
	url, err := p.queueURL(ctx, topic)
	if err != nil {
		return err
	}

	if _, err := p.api.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:          aws.String(url),
		MessageBody:       aws.String(string(payload)),
		MessageAttributes: attrs,
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
	attrs := traceAttributes(ctx)
	entries := make([]sqstypes.SendMessageBatchRequestEntry, len(payloads))

	for i, payload := range payloads {
		entries[i] = sqstypes.SendMessageBatchRequestEntry{
			MessageBody:       aws.String(string(payload)),
			MessageAttributes: attrs,
		}
	}

	return p.sendBatch(ctx, topic, entries)
}

// sendBatch sends entries to the queue named topic in SendMessageBatch calls of
// maxBatchSize, numbering each entry's Id by its index, and turns a transport error or
// any per-entry failure into a coded error.
func (p *Publisher) sendBatch(
	ctx context.Context,
	topic string,
	entries []sqstypes.SendMessageBatchRequestEntry,
) error {
	if len(entries) == 0 {
		return nil
	}

	url, err := p.queueURL(ctx, topic)
	if err != nil {
		return err
	}

	for i := range entries {
		// Id is the per-request entry id (unique within the batch); the index serves.
		entries[i].Id = aws.String(strconv.Itoa(i))
	}

	for start := 0; start < len(entries); start += maxBatchSize {
		out, err := p.api.SendMessageBatch(ctx, &awssqs.SendMessageBatchInput{
			QueueUrl: aws.String(url),
			Entries:  entries[start:min(start+maxBatchSize, len(entries))],
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

// withKey returns attrs plus key as the partition_key string attribute, leaving attrs
// (shared across a batch) unchanged.
func withKey(
	attrs map[string]sqstypes.MessageAttributeValue,
	key string,
) map[string]sqstypes.MessageAttributeValue {
	out := make(map[string]sqstypes.MessageAttributeValue, len(attrs)+1)
	maps.Copy(out, attrs)

	out[interfaces.PartitionKeyAttribute] = sqstypes.MessageAttributeValue{
		DataType:    aws.String("String"),
		StringValue: aws.String(key),
	}

	return out
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
