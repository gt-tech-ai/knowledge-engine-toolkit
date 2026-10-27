// Package sqs is the SQS OutboxSink: it routes each record to a queue, sends
// each queue's records with SendMessageBatch (at most 10 per request and 1 MiB
// of messages) and maps each entry's result back to its record.
package sqs

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// maxBatch is SendMessageBatch's per-request entry limit.
const maxBatch = 10

// maxBatchBytes is SendMessageBatch's per-request limit on the sum of its
// messages' sizes.
const maxBatchBytes = 1 << 20

// maxMessageBytes is SQS's limit on one message's size (body plus attributes).
const maxMessageBytes = 256 << 10

// fifoSuffix marks a FIFO queue's name.
const fifoSuffix = ".fifo"

// API is the part of the AWS SQS client the sink uses; *awssqs.Client satisfies it.
//
// SDK seam — the AWS SQS SDK client (aws-sdk-go-v2/service/sqs), cannot compose with a
// core port.
type API interface {
	// GetQueueUrl resolves an existing queue's URL by name.
	GetQueueUrl(
		ctx context.Context,
		in *awssqs.GetQueueUrlInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.GetQueueUrlOutput, error)
	// SendMessageBatch enqueues up to 10 messages, reporting each entry's outcome.
	SendMessageBatch(
		ctx context.Context,
		in *awssqs.SendMessageBatchInput,
		optFns ...func(*awssqs.Options),
	) (*awssqs.SendMessageBatchOutput, error)
}

// Config configures the SQS sink. At least one of Queue and Routes is required.
type Config struct {
	// Routes maps a record's route (its types.OutboxRouteAttribute attribute) to
	// the queue name it is sent to.
	Routes map[string]string `yaml:"routes" mapstructure:"routes"`
	// Queue is the queue a record without a route is sent to ("" = such a record
	// is rejected). Every queue must already exist. A queue whose name ends in
	// ".fifo" is a FIFO queue: each message's group id is the record's Key (else
	// its Tenant, else its Lane) and its deduplication id is the record's ID.
	Queue string `yaml:"queue" mapstructure:"queue"`
}

// Sink delivers outbox records to SQS queues chosen by each record's route.
// Each message's body is the record's payload (which must therefore be valid SQS
// message text) and it carries the outbox_id string attribute, plus tenant and
// lane when non-empty (SQS rejects an empty attribute value), so a consumer can
// deduplicate an at-least-once redelivery by outbox_id.
type Sink struct {
	// api is the (decorated) SQS client.
	api API
	// routes maps a route to its queue name.
	routes map[string]string
	// urls caches resolved queue URLs by queue name.
	urls map[string]string
	// queue is the queue for records without a route ("" = none).
	queue string
	// mu guards urls.
	mu sync.Mutex
}

// New builds the sink over api, the SDK client the composition root injects; a
// missing API, no queue and no routes, or a route mapped to an empty queue name is
// CodeInvalidInput.
func New(cfg Config, api API) (*Sink, error) {
	if api == nil || (cfg.Queue == "" && len(cfg.Routes) == 0) {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"outbox sqs sink: api and a queue or routes are required",
		)
	}
	routes := make(map[string]string, len(cfg.Routes))
	for route, queue := range cfg.Routes {
		if route == "" || queue == "" {
			return nil, apperr.New(apperr.CodeInvalidInput,
				"outbox sqs sink: route "+route+" needs a non-empty name and queue")
		}
		routes[route] = queue
	}
	return &Sink{
		api:    api,
		queue:  cfg.Queue,
		routes: routes,
		urls:   map[string]string{},
	}, nil
}

// Send groups recs by destination queue and delivers each group independently
// and concurrently, in batches of at most 10 messages and 1 MiB, so a failing
// queue fails only its own records. It returns one result per record: nil when
// sent, CodeInvalidInput for a record with an unknown route, one over SQS's
// 256 KiB message limit (never sent: no retry could succeed), or one the queue
// rejected as the sender's fault, CodeUnavailable for a server-side or
// request-level failure.
func (s *Sink) Send(ctx context.Context, recs []types.OutboxRecord) []error {
	results := make([]error, len(recs))
	groups := map[string][]int{}
	for i := range recs {
		queue, err := s.route(&recs[i])
		if err != nil {
			results[i] = err
			continue
		}
		groups[queue] = append(groups[queue], i)
	}
	var wg sync.WaitGroup
	for queue, idx := range groups {
		wg.Go(func() { s.sendQueue(ctx, queue, recs, idx, results) })
	}
	wg.Wait()
	return results
}

// route picks rec's queue: its route's queue, else the default queue.
func (s *Sink) route(rec *types.OutboxRecord) (string, error) {
	route := rec.Attributes[types.OutboxRouteAttribute]
	if route == "" && s.queue != "" {
		return s.queue, nil
	}
	if queue, ok := s.routes[route]; ok {
		return queue, nil
	}
	return "", apperr.New(
		apperr.CodeInvalidInput,
		"outbox sqs sink: no queue for route "+strconv.Quote(route),
	)
}

// sendQueue delivers the records at idx to queue, writing results[idx[...]].
// Each call writes a disjoint set of result slots, so groups run concurrently.
func (s *Sink) sendQueue(
	ctx context.Context,
	queue string,
	recs []types.OutboxRecord,
	idx []int,
	results []error,
) {
	url, err := s.queueURL(ctx, queue)
	if err != nil {
		for _, i := range idx {
			results[i] = err
		}
		return
	}
	for _, part := range packBatches(recs, idx, results) {
		batch := make([]types.OutboxRecord, len(part))
		for j, i := range part {
			batch[j] = recs[i]
		}
		out := make([]error, len(part))
		s.sendBatch(ctx, url, strings.HasSuffix(queue, fifoSuffix), batch, out)
		for j, i := range part {
			results[i] = out[j]
		}
	}
}

// packBatches splits the records at idx, in order, into SendMessageBatch-sized
// runs: at most maxBatch messages and maxBatchBytes of message size each. A
// record over maxMessageBytes is never sendable, so it gets CodeInvalidInput in
// results and joins no run.
func packBatches(
	recs []types.OutboxRecord,
	idx []int,
	results []error,
) [][]int {
	var runs [][]int
	var run []int
	size := 0
	for _, i := range idx {
		n := messageSize(&recs[i])
		if n > maxMessageBytes {
			results[i] = apperr.New(
				apperr.CodeInvalidInput,
				"outbox sqs sink: message of "+strconv.Itoa(n)+
					" bytes exceeds the SQS limit of "+strconv.Itoa(maxMessageBytes),
			)
			continue
		}
		if len(run) == maxBatch || (len(run) > 0 && size+n > maxBatchBytes) {
			runs = append(runs, run)
			run, size = nil, 0
		}
		run = append(run, i)
		size += n
	}
	if len(run) > 0 {
		runs = append(runs, run)
	}
	return runs
}

// messageSize is rec's size as SQS counts it against its limits: the body plus
// each message attribute's name, data type and value.
func messageSize(rec *types.OutboxRecord) int {
	n := len(rec.Payload)
	for name, v := range messageAttributes(rec) {
		n += len(name) + len(aws.ToString(v.DataType)) + len(aws.ToString(v.StringValue))
	}
	return n
}

// sendBatch sends one batch and writes each entry's outcome into results; fifo
// adds each entry's message group and deduplication ids.
func (s *Sink) sendBatch(
	ctx context.Context,
	url string,
	fifo bool,
	recs []types.OutboxRecord,
	results []error,
) {
	entries := make([]sqstypes.SendMessageBatchRequestEntry, len(recs))
	for i := range recs {
		rec := &recs[i]
		entries[i] = sqstypes.SendMessageBatchRequestEntry{
			Id:                aws.String(strconv.Itoa(i)),
			MessageBody:       aws.String(string(rec.Payload)),
			MessageAttributes: messageAttributes(rec),
		}
		if fifo {
			entries[i].MessageGroupId = aws.String(groupID(rec))
			entries[i].MessageDeduplicationId = aws.String(rec.ID.String())
		}
	}
	out, err := s.api.SendMessageBatch(
		ctx,
		&awssqs.SendMessageBatchInput{QueueUrl: aws.String(url), Entries: entries},
	)
	if err != nil {
		fill(
			results,
			apperr.Wrap(
				err,
				apperr.CodeOr(err, apperr.CodeUnavailable),
				"outbox sqs sink: send batch",
			),
		)
		return
	}
	fill(
		results,
		apperr.New(apperr.CodeInternal, "outbox sqs sink: no result for entry"),
	)
	for _, ok := range out.Successful {
		if i, valid := entryIndex(ok.Id, len(recs)); valid {
			results[i] = nil
		}
	}
	for _, failed := range out.Failed {
		i, valid := entryIndex(failed.Id, len(recs))
		if !valid {
			continue
		}
		code := apperr.CodeUnavailable
		if failed.SenderFault {
			code = apperr.CodeInvalidInput
		}
		results[i] = apperr.New(
			code,
			"outbox sqs sink: "+aws.ToString(
				failed.Code,
			)+": "+aws.ToString(
				failed.Message,
			),
		)
	}
}

// queueURL resolves and caches queue's URL; a failure is not cached. The lookup
// runs outside the lock so a slow queue never blocks another queue's lookup.
func (s *Sink) queueURL(ctx context.Context, queue string) (string, error) {
	s.mu.Lock()
	url, ok := s.urls[queue]
	s.mu.Unlock()
	if ok {
		return url, nil
	}
	out, err := s.api.GetQueueUrl(
		ctx,
		&awssqs.GetQueueUrlInput{QueueName: aws.String(queue)},
	)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeUnavailable),
			"outbox sqs sink: resolve queue "+queue,
		)
	}
	url = aws.ToString(out.QueueUrl)
	s.mu.Lock()
	s.urls[queue] = url
	s.mu.Unlock()
	return url, nil
}

// entryIndex parses a batch entry id back to its index, rejecting a foreign id.
func entryIndex(id *string, n int) (int, bool) {
	i, err := strconv.Atoi(aws.ToString(id))
	return i, err == nil && i >= 0 && i < n
}

// groupID is rec's FIFO message group: its Key, else its Tenant, else its Lane.
func groupID(rec *types.OutboxRecord) string {
	for _, id := range []string{rec.Key, rec.Tenant} {
		if id != "" {
			return id
		}
	}
	return rec.Lane
}

// messageAttributes is rec's outbox_id, tenant and lane String attributes. SQS
// rejects an attribute with an empty value, so an empty tenant or lane is omitted.
func messageAttributes(
	rec *types.OutboxRecord,
) map[string]sqstypes.MessageAttributeValue {
	attrs := map[string]sqstypes.MessageAttributeValue{
		"outbox_id": stringAttr(rec.ID.String()),
	}
	if rec.Tenant != "" {
		attrs["tenant"] = stringAttr(rec.Tenant)
	}
	if rec.Lane != "" {
		attrs["lane"] = stringAttr(rec.Lane)
	}
	return attrs
}

// stringAttr is a String message attribute.
func stringAttr(v string) sqstypes.MessageAttributeValue {
	return sqstypes.MessageAttributeValue{
		DataType:    aws.String("String"),
		StringValue: aws.String(v),
	}
}

// fill sets every result to err.
func fill(results []error, err error) {
	for i := range results {
		results[i] = err
	}
}
