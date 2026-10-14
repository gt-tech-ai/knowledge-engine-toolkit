package sqs

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/lifecycle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
)

// Compile-time interface assertion.
var _ interfaces.MessageConsumer = (*Subscriber)(nil)

const (
	// defaultBaseBackoff is the initial wait after a transient error before the
	// receive loop retries; it doubles up to maxBackoff.
	defaultBaseBackoff = 500 * time.Millisecond
	// maxBackoff caps the retry wait so a long outage does not stall recovery.
	maxBackoff = 10 * time.Second
	// longPollSeconds is the default SQS long-poll wait, which lowers empty-receive
	// churn; Config.WaitTimeSeconds overrides it.
	longPollSeconds = 10
	// maxWaitTimeSeconds is the longest long-poll wait SQS accepts.
	maxWaitTimeSeconds = 20
	// maxVisibilityTimeout is the longest visibility timeout SQS accepts.
	maxVisibilityTimeout = 12 * time.Hour
	// maxMessagesPerReceive is the batch size per ReceiveMessage.
	maxMessagesPerReceive = 10
	// maxConcurrentHandlers bounds how many messages from one receive batch are
	// handled concurrently. The batch is capped at maxMessagesPerReceive, so this
	// fans the whole batch out while keeping the live goroutine count bounded.
	maxConcurrentHandlers = maxMessagesPerReceive
	// handlerGracePeriod bounds an in-flight handler+ack so a shutdown drain can't
	// hang on a stuck message; it runs on a context decoupled from loop cancellation.
	handlerGracePeriod = 15 * time.Second
)

// Subscriber consumes SQS messages via the AWS SDK. Each Subscribe call starts a
// long-poll goroutine that resolves (and creates, if absent) the queue, then
// loops receive → handle → delete. Transient errors are logged and retried with
// backoff — the loop never exits on them — so invalidation cannot be silently
// disabled by a momentary outage. Close cancels all subscriptions and waits for
// in-flight handlers to drain.
type Subscriber struct {
	// NoOp supplies the no-op Start/Stop: the long-poll receive loop is owned
	// per-Subscribe call (cancelled via cancels), so the Subscriber itself opens/closes
	// no shared resource.
	lifecycle.NoOp

	// api is the SQS API used for receive/delete (the real SDK client or a test double).
	api API
	// ready gates each receive; nil receives unconditionally.
	ready func(ctx context.Context) error
	// logger reports receive/handle errors surfaced by the poll goroutines.
	logger interfaces.Logger
	// handlers runs each handle through the client tier (op = the topic).
	handlers client.Base
	// cancels holds the cancel func for each Subscribe's poll goroutine, cancelled by Close.
	cancels []context.CancelFunc
	// wg tracks the poll goroutines so Close can wait for in-flight handlers to drain.
	wg sync.WaitGroup
	// nackDelay bounds the jittered visibility delay of a failed message.
	nackDelay NackDelay
	// baseBackoff is the initial receive-retry wait for transient errors.
	baseBackoff time.Duration
	// mu guards cancels against concurrent Subscribe/Close.
	mu sync.Mutex
	// waitTimeSeconds is the long-poll wait of each receive.
	waitTimeSeconds int32
}

// NewSubscriber creates an SQS message subscriber. It uses Config.API when set,
// otherwise builds the real AWS SDK client from cfg, loading the AWS config under
// ctx. Config.BaseBackoff overrides the initial receive-retry wait and
// Config.WaitTimeSeconds the long-poll wait (0 uses each package default);
// Config.Ready gates receiving and Config.NackDelay delays a failed message's
// redelivery. Out-of-range poll settings are a CodeInvalidInput error.
func NewSubscriber(ctx context.Context, cfg Config) (*Subscriber, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	api, err := resolveAPI(ctx, cfg)
	if err != nil {
		return nil, err
	}
	baseBackoff := cfg.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultBaseBackoff
	}
	wait := cfg.WaitTimeSeconds
	if wait == 0 {
		wait = longPollSeconds
	}
	return &Subscriber{
		api:             api,
		ready:           cfg.Ready,
		logger:          cfg.Logger,
		handlers:        cfg.Handlers,
		nackDelay:       cfg.NackDelay,
		baseBackoff:     baseBackoff,
		waitTimeSeconds: wait,
	}, nil
}

// Subscribe starts a long-poll consumer goroutine for topic (an SQS queue name).
// It returns immediately; the goroutine runs until Close (or the passed context)
// cancels it.
func (s *Subscriber) Subscribe(
	ctx context.Context,
	topic string,
	handler interfaces.MessageHandler,
) error {
	loopCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancels = append(s.cancels, cancel)
	s.mu.Unlock()

	s.wg.Add(1)
	go s.consume(loopCtx, topic, handler)
	return nil
}

// Close cancels every subscription and waits for in-flight handlers to drain.
func (s *Subscriber) Close() error {
	s.mu.Lock()
	for _, cancel := range s.cancels {
		cancel()
	}
	s.cancels = nil
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}

// consume runs the receive → handle → delete loop for one topic until ctx is
// done. It resolves the queue lazily (retrying on failure) so a not-yet-ready
// ElasticMQ at boot does not permanently disable the subscription.
func (s *Subscriber) consume(
	ctx context.Context,
	topic string,
	handler interfaces.MessageHandler,
) {
	defer s.wg.Done()

	var queueURL string
	backoff := s.baseBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		if queueURL == "" {
			url, err := ensureQueue(ctx, s.api, topic)
			if err != nil {
				if !s.backoffOrDone(ctx, "ensure queue", topic, err, &backoff) {
					return
				}
				continue
			}
			queueURL = url
			backoff = s.baseBackoff
		}

		// A failing gate pauses receiving: nothing is received, so no message's
		// receive count rises while the consumer's downstream is unavailable.
		if s.ready != nil {
			if err := s.ready(ctx); err != nil {
				if !s.backoffOrDone(ctx, "ready", topic, err, &backoff) {
					return
				}
				continue
			}
		}

		out, err := s.api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:              aws.String(queueURL),
			MaxNumberOfMessages:   maxMessagesPerReceive,
			WaitTimeSeconds:       s.waitTimeSeconds,
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			if !s.backoffOrDone(ctx, "receive", topic, err, &backoff) {
				return
			}
			continue
		}
		backoff = s.baseBackoff

		s.handleBatch(ctx, topic, queueURL, handler, out.Messages)
	}
}

// handleBatch fans the messages of one receive batch out to the handler with
// bounded concurrency (≤ maxConcurrentHandlers live goroutines), then acks the
// successfully-handled subset in a single DeleteMessageBatch. Handlers
// run on grace-bounded contexts decoupled from the loop's cancellation, so an
// in-flight batch drains when Close cancels the receive loop rather than aborting
// mid-flight.
func (s *Subscriber) handleBatch(
	ctx context.Context,
	topic, queueURL string,
	handler interfaces.MessageHandler,
	messages []sqstypes.Message,
) {
	if len(messages) == 0 {
		return
	}
	var (
		mu      sync.Mutex
		handled []sqstypes.Message
		wg      sync.WaitGroup
	)
	sem := make(chan struct{}, maxConcurrentHandlers)
	for _, m := range messages {
		// Acquire a slot before spawning so at most maxConcurrentHandlers
		// goroutines are live at once (bounded fan-out).
		sem <- struct{}{}
		wg.Add(1)
		go func(m sqstypes.Message) {
			defer wg.Done()
			defer func() { <-sem }()
			if !s.runHandler(ctx, topic, handler, m) {
				s.delayRedelivery(ctx, topic, queueURL, m)
				return
			}
			mu.Lock()
			handled = append(handled, m)
			mu.Unlock()
		}(m)
	}
	wg.Wait()
	s.deleteHandled(ctx, topic, queueURL, handled)
}

// runHandler dispatches one message to the handler on a grace-bounded context
// decoupled from the loop's cancellation, and reports whether it was handled
// successfully (and so should be acked). A handler error leaves the message for
// redelivery (nack) and is logged. Each handle runs through the client tier as the
// operation named by its topic, continuing the trace the publisher put in the message's
// attributes.
func (s *Subscriber) runHandler(
	ctx context.Context,
	topic string,
	handler interfaces.MessageHandler,
	m sqstypes.Message,
) bool {
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerGracePeriod)
	defer cancel()
	msg := &interfaces.Message{
		ID:       aws.ToString(m.MessageId),
		Topic:    topic,
		Payload:  []byte(aws.ToString(m.Body)),
		Metadata: stringAttributes(m.MessageAttributes),
	}
	hctx = tracer.Extract(hctx, msg.Metadata)
	err := s.handlers.DoOnce(hctx, topic, func(ctx context.Context) error {
		return handler(ctx, msg)
	})
	if err != nil {
		s.logError("handle", topic, err)
	}
	return err == nil
}

// stringAttributes returns a received message's string attributes (the publisher's
// trace context among them), or nil when it has none.
func stringAttributes(attrs map[string]sqstypes.MessageAttributeValue) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(attrs))
	for k, v := range attrs {
		if v.StringValue != nil {
			out[k] = *v.StringValue
		}
	}
	return out
}

// delayRedelivery hides a nacked message for a uniformly jittered delay in
// [nackDelay.Min, nackDelay.Max] so a transient failure neither waits out the
// queue's full visibility timeout nor redelivers a burst in lock-step. It is a
// no-op when the nack delay is disabled; a failed call is logged and the message
// keeps the queue's visibility timeout.
func (s *Subscriber) delayRedelivery(
	ctx context.Context,
	topic, queueURL string,
	m sqstypes.Message,
) {
	if s.nackDelay.Max <= 0 {
		return
	}
	delay := s.nackDelay.Min + retry.FullJitter(s.nackDelay.Max-s.nackDelay.Min)
	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerGracePeriod)
	defer cancel()
	_, err := s.api.ChangeMessageVisibility(vctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(queueURL),
		ReceiptHandle:     m.ReceiptHandle,
		VisibilityTimeout: visibilitySeconds(delay),
	})
	if err != nil {
		s.logError("change visibility", topic, err)
	}
}

// visibilitySeconds truncates d to the whole seconds SQS takes, clamped to
// [0, maxVisibilityTimeout] (validate already bounds the nack delay).
func visibilitySeconds(d time.Duration) int32 {
	secs := int64(min(d, maxVisibilityTimeout) / time.Second)
	switch {
	case secs <= 0:
		return 0
	case secs > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(secs)
	}
}

// deleteHandled acks the handled subset of a receive batch in one
// DeleteMessageBatch. The receive batch is capped at maxMessagesPerReceive
// (≤ maxBatchSize), so no chunking is needed. A batch-level error or any
// per-entry failure is logged — the affected message simply redelivers.
func (s *Subscriber) deleteHandled(
	ctx context.Context,
	topic, queueURL string,
	handled []sqstypes.Message,
) {
	if len(handled) == 0 {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), handlerGracePeriod)
	defer cancel()
	entries := make([]sqstypes.DeleteMessageBatchRequestEntry, len(handled))
	for i, m := range handled {
		entries[i] = sqstypes.DeleteMessageBatchRequestEntry{
			Id:            aws.String(strconv.Itoa(i)),
			ReceiptHandle: m.ReceiptHandle,
		}
	}
	out, err := s.api.DeleteMessageBatch(dctx, &awssqs.DeleteMessageBatchInput{
		QueueUrl: aws.String(queueURL),
		Entries:  entries,
	})
	if err != nil {
		s.logError("delete batch", topic, err)
		return
	}
	if len(out.Failed) > 0 {
		s.logError(
			"delete batch", topic,
			failedEntriesError("sqs delete message batch", out.Failed),
		)
	}
}

// backoffOrDone logs the error and sleeps for the current backoff, doubling it
// up to maxBackoff. Returns false if the context was cancelled.
func (s *Subscriber) backoffOrDone(
	ctx context.Context,
	op, topic string,
	err error,
	backoff *time.Duration,
) bool {
	if ctx.Err() != nil {
		return false
	}
	s.logError(op, topic, err)
	// Sleep the current backoff with equal jitter so multiple subscribers
	// recovering from the same outage don't retry in lock-step.
	if !sleepCtx(ctx, retry.EqualJitter(*backoff)) {
		return false
	}
	*backoff *= 2
	if *backoff > maxBackoff {
		*backoff = maxBackoff
	}
	return true
}

// logError logs a subscriber error when a logger is configured.
func (s *Subscriber) logError(op, topic string, err error) {
	if s.logger != nil {
		s.logger.Error("sqs subscriber error", "op", op, "topic", topic, "error", err)
	}
}

// sleepCtx sleeps for d or until ctx is done. Returns false if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
