package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/sqs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// receiveKeyed subscribes to topic on broker and returns the first n messages.
func receiveKeyed(
	t *testing.T,
	broker *memory.Broker,
	topic string,
	count int,
) []*interfaces.Message {
	t.Helper()

	sub := memory.NewSubscriber(broker, &client.Base{})
	got := make(chan *interfaces.Message, count)

	go func() {
		_ = sub.Subscribe(context.Background(), topic,
			func(_ context.Context, msg *interfaces.Message) error {
				got <- msg

				return nil
			})
	}()

	t.Cleanup(func() { _ = sub.Close() })

	out := make([]*interfaces.Message, 0, count)
	for range count {
		select {
		case msg := <-got:
			out = append(out, msg)
		case <-time.After(time.Second):
			t.Fatalf("received %d of %d messages", len(out), count)
		}
	}

	return out
}

// TestMemoryPublisher_KeyedKeepsOrderAndKey tests the memory backend's keyed publish.
//
// Why this test is important:
//   - The memory backend stands in for a partitioned broker in tests; it must carry the
//     key and keep per-key order, or tests of ordered consumers would pass on a broker
//     that never orders
//
// What it tests:
//   - PublishBatchKeyed then PublishKeyed deliver in publish order, each message's
//     partition_key metadata is its key, and OrderedPerKey is true
func TestMemoryPublisher_KeyedKeepsOrderAndKey(t *testing.T) {
	t.Parallel()

	broker := memory.NewBroker()
	pub := memory.NewPublisher(broker)
	ctx := context.Background()

	require.NoError(t, pub.PublishBatchKeyed(ctx, textT, []interfaces.KeyedPayload{
		{Key: textDoc1, Payload: []byte(textA)},
		{Key: "doc-2", Payload: []byte(textB)},
		{Key: textDoc1, Payload: []byte(textC)},
	}))
	require.NoError(t, pub.PublishKeyed(ctx, textT, textDoc1, []byte(textDLower)))

	msgs := receiveKeyed(t, broker, textT, receiveKeyedInput)
	bodies := make([]string, len(msgs))
	keys := make([]string, len(msgs))

	for i, msg := range msgs {
		bodies[i] = string(msg.Payload)
		keys[i] = msg.Metadata[interfaces.PartitionKeyAttribute]
	}

	assert.Equal(t, []string{textA, textB, textC, textDLower}, bodies)
	assert.Equal(t, []string{textDoc1, "doc-2", textDoc1, textDoc1}, keys)
	assert.True(t, pub.OrderedPerKey())
}

// TestSQSPublisher_KeyedSendsThePartitionKeyAttribute tests the SQS keyed publish.
//
// Why this test is important:
//   - A standard queue cannot order by key, but a consumer (or a later move to a
//     partitioned broker) still needs the key; dropping it would lose the grouping
//
// What it tests:
//   - PublishKeyed sends the key as the partition_key String attribute;
//     PublishBatchKeyed sends 12 items in batches of 10 and 2, each entry carrying its
//     own key with unique ids; OrderedPerKey is false
func TestSQSPublisher_KeyedSendsThePartitionKeyAttribute(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockAPI(ctrl)
	api.EXPECT().GetQueueUrl(gomock.Any(), gomock.Any()).
		Return(&awssqs.GetQueueUrlOutput{QueueUrl: aws.String("http://q")}, nil)
	api.EXPECT().SendMessage(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context,
			sendMessageInput *awssqs.SendMessageInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageOutput, error) {
			assert.Equal(
				t,
				"doc-9",
				aws.ToString(
					sendMessageInput.MessageAttributes[interfaces.PartitionKeyAttribute].StringValue,
				),
			)
			assert.Equal(
				t,
				"String",
				aws.ToString(
					sendMessageInput.MessageAttributes[interfaces.PartitionKeyAttribute].DataType,
				),
			)

			return &awssqs.SendMessageOutput{}, nil
		})

	var sizes []int

	ids := map[string]bool{}

	var keys []string

	testSQSPublisherKeyedSendsThePartitionKeyAttributeBatches(t, api, sizes, ids, keys)
}

// testSQSPublisherKeyedSendsThePartitionKeyAttributeBatches continues
// TestSQSPublisher_KeyedSendsThePartitionKeyAttribute: batches.
func testSQSPublisherKeyedSendsThePartitionKeyAttributeBatches(
	t *testing.T,
	api *mocks.MockAPI,
	sizes []int,
	ids map[string]bool,
	keys []string,
) {
	t.Helper()

	api.EXPECT().SendMessageBatch(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(
		func(
			_ context.Context,
			in *awssqs.SendMessageBatchInput,
			_ ...func(*awssqs.Options),
		) (*awssqs.SendMessageBatchOutput, error) {
			sizes = append(sizes, len(in.Entries))
			for _, e := range in.Entries {
				ids[aws.ToString(e.Id)] = true
				keys = append(
					keys,
					aws.ToString(
						e.MessageAttributes[interfaces.PartitionKeyAttribute].StringValue,
					),
				)
			}

			return &awssqs.SendMessageBatchOutput{}, nil
		})

	pub := fixtures.Must(sqs.NewPublisher(t.Context(), &sqs.Config{API: api}))
	ctx := context.Background()

	require.NoError(t, pub.PublishKeyed(ctx, textQ, "doc-9", []byte(textX)))

	items := make([]interfaces.KeyedPayload, makeInputBatches)
	want := make([]string, makeInputBatches)

	for i := range items {
		want[i] = []string{"even", "odd"}[i%wantI]
		items[i] = interfaces.KeyedPayload{Key: want[i], Payload: []byte(textP)}
	}

	require.NoError(t, pub.PublishBatchKeyed(ctx, textQ, items))

	assert.Equal(t, []int{10, 2}, sizes)
	assert.Len(t, ids, wantIDsLen)
	assert.Equal(t, want, keys)
	assert.False(t, pub.OrderedPerKey())
}

// TestDecorate_KeepsTheKeyedCapability tests that decoration forwards keyed publishing.
//
// Why this test is important:
//   - Every publisher is decorated at the composition root; a wrapper that dropped
//     the keyed methods would silently turn keyed publishing off in production
//
// What it tests:
//   - Decorate over a keyed base returns an interfaces.KeyedPublisher whose keyed
//     publishes reach the base with their keys and whose OrderedPerKey is the base's;
//     Decorate over an unkeyed base does not claim the capability
func TestDecorate_KeepsTheKeyedCapability(t *testing.T) {
	t.Parallel()

	deps := decorate.Fixed(&decorate.Deps{Timeout: time.Minute})
	broker := memory.NewBroker()
	pub := fixtures.Must(
		messaging.Decorate(memory.NewPublisher(broker), deps, textP, client.Producer()),
	)
	keyed, found := pub.(interfaces.KeyedPublisher)
	require.True(t, found)

	ctx := context.Background()

	require.NoError(t, keyed.PublishKeyed(ctx, textT, "k1", []byte(textA)))
	require.NoError(t, keyed.PublishBatchKeyed(ctx, textT,
		[]interfaces.KeyedPayload{{Key: "k2", Payload: []byte(textB)}}))

	msgs := receiveKeyed(t, broker, textT, receiveKeyedInputCapability)
	assert.Equal(t, "k1", msgs[0].Metadata[interfaces.PartitionKeyAttribute])
	assert.Equal(t, "k2", msgs[1].Metadata[interfaces.PartitionKeyAttribute])
	assert.True(t, keyed.OrderedPerKey())

	ctrl := gomock.NewController(t)
	plain := fixtures.Must(
		messaging.Decorate(mocks.NewMockMessagePublisher(ctrl), deps, textP),
	)

	_, found = plain.(interfaces.KeyedPublisher)
	assert.False(t, found)
}

const (
	// makeInputBatches is the make input batches the tests use.
	makeInputBatches = 12

	// receiveKeyedInput is the receive keyed input the tests use.
	receiveKeyedInput = 4

	// receiveKeyedInputCapability is the receive keyed input capability the tests use.
	receiveKeyedInputCapability = 2

	// wantIDsLen is the expected ids len.
	wantIDsLen = 12
)
