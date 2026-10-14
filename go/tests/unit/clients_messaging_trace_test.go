package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// traceOf returns the trace id of the span context ctx carries.
func traceOf(ctx context.Context) oteltrace.TraceID {
	return oteltrace.SpanContextFromContext(ctx).TraceID()
}

// TestMemoryMessaging_HandlerContinuesThePublishersTrace tests trace propagation across
// the in-memory broker.
//
// Why this test is important:
//   - Without the producer's trace context on the message, every consumer starts a new
//     trace and the work a request queued cannot be followed from it.
//
// What it tests:
//   - A handler's context carries the publisher's trace id.
func TestMemoryMessaging_HandlerContinuesThePublishersTrace(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	sc := sampledSpanContext()
	ctx := oteltrace.ContextWithSpanContext(t.Context(), sc)
	// The consumer runs on its own context, with no span of the publisher's.
	consumer := oteltrace.ContextWithSpanContext(ctx, oteltrace.SpanContext{})
	broker := memory.NewBroker()
	require.NoError(t, memory.NewPublisher(broker).Publish(ctx, "topic", []byte("x")))
	sub := memory.NewSubscriber(broker, client.Base{})
	got := make(chan oteltrace.TraceID, 1)
	go func() {
		_ = sub.Subscribe(consumer, "topic",
			func(ctx context.Context, _ *interfaces.Message) error {
				got <- traceOf(ctx)
				return nil
			})
	}()
	assert.Equal(t, sc.TraceID(), recv(t, got, "the handler to run"))
	require.NoError(t, sub.Close())
}

// TestSQSMessaging_HandlerContinuesThePublishersTrace tests trace propagation across an
// SQS queue.
//
// Why this test is important:
//   - Without the producer's trace context on the message, every consumer starts a new
//     trace and an upload's ingestion, deletion and notification work cannot be
//     followed from the request that caused it.
//
// What it tests:
//   - Publish and PublishBatch send the traceparent as a string message attribute.
//   - The subscriber asks for every attribute, hands them to the handler as Metadata,
//     and runs the handler on a context carrying the publisher's trace id.
func TestSQSMessaging_HandlerContinuesThePublishersTrace(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	sc := sampledSpanContext()
	ctx := oteltrace.ContextWithSpanContext(t.Context(), sc)
	// The consumer runs on its own context, with no span of the publisher's.
	consumer := oteltrace.ContextWithSpanContext(ctx, oteltrace.SpanContext{})
	api := mocks.NewMockAPI(gomock.NewController(t))
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil).
		AnyTimes()
	var sent map[string]sqstypes.MessageAttributeValue
	api.EXPECT().SendMessage(gomock.Any(), gomock.Any()).DoAndReturn(func(
		_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options),
	) (*awssqs.SendMessageOutput, error) {
		sent = in.MessageAttributes
		return &awssqs.SendMessageOutput{}, nil
	})
	var batched map[string]sqstypes.MessageAttributeValue
	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).DoAndReturn(func(
		_ context.Context, in *awssqs.SendMessageBatchInput, _ ...func(*awssqs.Options),
	) (*awssqs.SendMessageBatchOutput, error) {
		batched = in.Entries[0].MessageAttributes
		return &awssqs.SendMessageBatchOutput{}, nil
	})
	pub, err := sqs.NewPublisher(ctx, sqs.Config{API: api})
	require.NoError(t, err)
	require.NoError(t, pub.Publish(ctx, "q", []byte("x")))
	require.NoError(t, pub.PublishBatch(ctx, "q", [][]byte{[]byte("x")}))
	require.Contains(t, sent, "traceparent")
	assert.Equal(t, "String", aws.ToString(sent["traceparent"].DataType))
	assert.Contains(
		t,
		aws.ToString(sent["traceparent"].StringValue),
		sc.TraceID().String(),
	)
	assert.Equal(t, sent, batched)

	var asked []string
	api.EXPECT().ReceiveMessage(gomock.Any(), gomock.Any()).DoAndReturn(func(
		ctx context.Context, in *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options),
	) (*awssqs.ReceiveMessageOutput, error) {
		if asked == nil {
			asked = in.MessageAttributeNames
			return &awssqs.ReceiveMessageOutput{Messages: []sqstypes.Message{{
				MessageId: aws.String("m1"), Body: aws.String("x"),
				ReceiptHandle: aws.String("rh"), MessageAttributes: sent,
			}}}, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}).AnyTimes()
	api.EXPECT().DeleteMessageBatch(gomock.Any(), gomock.Any()).
		Return(&awssqs.DeleteMessageBatchOutput{}, nil).AnyTimes()
	sub, err := sqs.NewSubscriber(
		ctx,
		sqs.Config{API: api, BaseBackoff: time.Millisecond},
	)
	require.NoError(t, err)
	type delivery struct {
		metadata map[string]string
		trace    oteltrace.TraceID
	}
	got := make(chan delivery, 1)
	require.NoError(t, sub.Subscribe(consumer, "q",
		func(ctx context.Context, m *interfaces.Message) error {
			got <- delivery{trace: traceOf(ctx), metadata: m.Metadata}
			return nil
		}))
	d := recv(t, got, "the handler to run")
	require.NoError(t, sub.Close())
	assert.Equal(t, []string{"All"}, asked)
	assert.Equal(t, sc.TraceID(), d.trace)
	assert.Equal(
		t,
		aws.ToString(sent["traceparent"].StringValue),
		d.metadata["traceparent"],
	)
}
