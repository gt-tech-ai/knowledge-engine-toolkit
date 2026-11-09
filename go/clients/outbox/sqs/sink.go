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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
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
	// FairQueues names the standard queues that are SQS fair queues: each message
	// sent to one carries the same group id a FIFO message would (but no
	// deduplication id), so SQS serves the other groups ahead of one group's backlog.
	FairQueues []string `yaml:"fair_queues" mapstructure:"fair_queues"`
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
	// fair holds the queue names that are SQS fair queues.
	fair map[string]bool
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

	routes, err := copyRoutes(cfg.Routes)
	if err != nil {
		return nil, err
	}

	fair := make(map[string]bool, len(cfg.FairQueues))
	for _, queue := range cfg.FairQueues {
		fair[queue] = true
	}

	return &Sink{
		api:    api,
		queue:  cfg.Queue,
		routes: routes,
		fair:   fair,
		urls:   map[string]string{},
	}, nil
}

// copyRoutes copies the route table, rejecting an empty name or queue.
func copyRoutes(in map[string]string) (map[string]string, error) {
	routes := make(map[string]string, len(in))

	for route, queue := range in {
		if route == "" || queue == "" {
			return nil, apperr.New(apperr.CodeInvalidInput,
				"outbox sqs sink: route "+route+" needs a non-empty name and queue")
		}

		routes[route] = queue
	}

	return routes, nil
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
		s.sendPart(ctx, url, queue, recs, part, results)
	}
}

// sendPart sends one packed batch and copies its outcomes back to results.
func (s *Sink) sendPart(
	ctx context.Context,
	url, queue string,
	recs []types.OutboxRecord,
	part []int,
	results []error,
) {
	batch := make([]types.OutboxRecord, len(part))
	for j, i := range part {
		batch[j] = recs[i]
	}

	out := make([]error, len(part))
	s.sendBatch(ctx, url, s.delivery(queue), batch, out)

	for j, i := range part {
		results[i] = out[j]
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
	var (
		runs [][]int
		run  []int
	)

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

		if batchFull(len(run), size, n) {
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

// batchFull reports whether a message of n bytes must start a new batch.
func batchFull(count, size, n int) bool {
	return count == maxBatch || (count > 0 && size+n > maxBatchBytes)
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

// delivery is how messages to queue are sent: grouped and deduplicated on a FIFO
// queue, grouped only on a fair queue, neither on any other standard queue.
type delivery struct {
	// grouped adds each message's group id.
	grouped bool
	// deduplicated adds each message's deduplication id.
	deduplicated bool
}

// delivery returns how messages to queue are sent.
func (s *Sink) delivery(queue string) delivery {
	fifo := strings.HasSuffix(queue, fifoSuffix)

	return delivery{grouped: fifo || s.fair[queue], deduplicated: fifo}
}

// sendBatch sends one batch and writes each entry's outcome into results; how
// adds each entry's message group and deduplication ids.
func (s *Sink) sendBatch(
	ctx context.Context,
	url string,
	how delivery,
	recs []types.OutboxRecord,
	results []error,
) {
	out, err := s.api.SendMessageBatch(
		ctx,
		&awssqs.SendMessageBatchInput{
			QueueUrl: aws.String(url),
			Entries:  batchEntries(how, recs),
		},
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
		if i, valid := entryIndex(failed.Id, len(recs)); valid {
			results[i] = batchEntryError(&failed)
		}
	}
}

// batchEntries builds the request entries, indexed by position.
func batchEntries(
	how delivery, recs []types.OutboxRecord,
) []sqstypes.SendMessageBatchRequestEntry {
	entries := make([]sqstypes.SendMessageBatchRequestEntry, len(recs))

	for i := range recs {
		rec := &recs[i]

		entries[i] = sqstypes.SendMessageBatchRequestEntry{
			Id:                aws.String(strconv.Itoa(i)),
			MessageBody:       aws.String(string(rec.Payload)),
			MessageAttributes: messageAttributes(rec),
		}

		if how.grouped {
			entries[i].MessageGroupId = aws.String(groupID(rec))
		}

		if how.deduplicated {
			entries[i].MessageDeduplicationId = aws.String(rec.ID.String())
		}
	}

	return entries
}

// batchEntryError codes a failed entry: the sender's fault is invalid input, anything
// else is transient.
func batchEntryError(failed *sqstypes.BatchResultErrorEntry) error {
	code := apperr.CodeUnavailable
	if failed.SenderFault {
		code = apperr.CodeInvalidInput
	}

	return apperr.New(
		code,
		"outbox sqs sink: "+aws.ToString(failed.Code)+": "+aws.ToString(failed.Message),
	)
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

// messageAttributes is rec's outbox_id, tenant, lane and partition_key (its Key)
// String attributes. SQS rejects an attribute with an empty value, so an empty
// tenant, lane or key is omitted.
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

	if rec.Key != "" {
		attrs[interfaces.PartitionKeyAttribute] = stringAttr(rec.Key)
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
