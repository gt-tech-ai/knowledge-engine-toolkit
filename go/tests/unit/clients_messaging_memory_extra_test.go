package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TestMessagingMemory_RespectsContextCancellation tests that the in-memory
// publisher honors a cancelled context instead of blocking forever when the topic
// buffer is full, on both the single and batch publish paths.
//
// Why this test is important:
//   - The memory backend stands in for SQS in dev/test; a publish that ignored ctx
//     cancellation on a saturated topic would hang the caller (and the test) rather
//     than returning promptly like the real SQS client does.
//
// What it tests:
//   - With the topic buffer full and a cancelled context, Publish and PublishBatch
//     each return the context error rather than blocking.
func TestMessagingMemory_RespectsContextCancellation(t *testing.T) {
	t.Parallel()

	pub := memory.NewPublisher(memory.NewBroker())
	// Fill the topic buffer (no subscriber drains it) so the next send would block.
	for range 1024 {
		require.NoError(t, pub.Publish(context.Background(), "full", []byte("x")))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.Error(t, pub.Publish(ctx, "full", []byte("y")),
		"a full buffer + cancelled context must not block")
	require.Error(t, pub.PublishBatch(ctx, "full", [][]byte{[]byte("a"), []byte("b")}),
		"batch publish must surface the first send's context error")
}

// TestMessagingMemory_PublishBatchDelivers tests that PublishBatch delivers every
// payload in order to a subscriber on the same broker.
//
// Why this test is important:
//   - Batch publish is the fan-out path for multi-event emits; dropping or reordering
//     a payload would silently lose events the real SQS batch send would deliver.
//
// What it tests:
//   - PublishBatch of three payloads yields three handler deliveries in order.
func TestMessagingMemory_PublishBatchDelivers(t *testing.T) {
	t.Parallel()

	broker := memory.NewBroker()
	pub := memory.NewPublisher(broker)
	sub := memory.NewSubscriber(broker, client.Base{})
	got := make(chan []byte, 3)
	go func() {
		_ = sub.Subscribe(context.Background(), "batch",
			func(_ context.Context, m *interfaces.Message) error {
				got <- m.Payload
				return nil
			})
	}()

	require.NoError(t, pub.PublishBatch(context.Background(), "batch",
		[][]byte{[]byte("a"), []byte("b"), []byte("c")}))

	for _, want := range []string{"a", "b", "c"} {
		select {
		case p := <-got:
			assert.Equal(t, want, string(p))
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for payload %q", want)
		}
	}
	require.NoError(t, sub.Close())
}

// TestMessagingMemory_SubscribeStopsOnContextCancel tests that a subscriber returns
// its context error when the context is cancelled (the non-Close exit path).
//
// Why this test is important:
//   - A subscriber must stop when its context ends, not only when Close is called;
//     otherwise a shutting-down consumer would leak a goroutine on the topic.
//
// What it tests:
//   - Subscribe returns a context error once the passed context is cancelled.
func TestMessagingMemory_SubscribeStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	sub := memory.NewSubscriber(memory.NewBroker(), client.Base{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sub.Subscribe(ctx, "topic",
			func(context.Context, *interfaces.Message) error { return nil })
	}()

	cancel()
	select {
	case err := <-done:
		require.Error(t, err, "a cancelled context must end the subscribe loop")
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not return after context cancellation")
	}
}
