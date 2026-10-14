package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TestMessagingMemory_PublishReceive tests that a message published to a topic reaches a
// subscriber built from the SAME broker, and that a separate broker is isolated.
//
// Why this test is important:
//   - The memory backend only stands in for SQS if a publish reaches a subscriber on the
//     same broker; and separate brokers must NOT share a queue, or parallel tests reusing
//     a topic name would cross-talk. Both properties must hold.
//
// What it tests:
//   - publish → the subscriber's handler receives the payload; a subscriber on a
//     different broker receives nothing within the window.
func TestMessagingMemory_PublishReceive(t *testing.T) {
	t.Parallel()

	broker := memory.NewBroker()
	pub := memory.NewPublisher(broker)
	sub := memory.NewSubscriber(broker, client.Base{})
	received := make(chan []byte, 1)
	var handler interfaces.MessageHandler = func(
		_ context.Context,
		msg *interfaces.Message,
	) error {
		received <- msg.Payload
		return nil
	}

	require.NoError(t, pub.Publish(context.Background(), "topic", []byte("hello")))
	go func() { _ = sub.Subscribe(context.Background(), "topic", handler) }()

	select {
	case got := <-received:
		require.Equal(t, []byte("hello"), got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the published message")
	}
	require.NoError(t, sub.Close())

	// Isolation: a subscriber on a DIFFERENT broker sees none of the first broker's
	// messages.
	other := memory.NewSubscriber(memory.NewBroker(), client.Base{})
	otherRecv := make(chan []byte, 1)
	go func() {
		_ = other.Subscribe(
			context.Background(),
			"topic",
			func(_ context.Context, msg *interfaces.Message) error {
				otherRecv <- msg.Payload
				return nil
			},
		)
	}()
	select {
	case <-otherRecv:
		t.Fatal("a separate broker must not see the first broker's messages")
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, other.Close())
}
