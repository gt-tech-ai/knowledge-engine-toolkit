package unit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
)

// prevTraceparent is the previous turn's root span used across the conversation tests.
const prevTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// TestInjectConversation_SetsBaggageKeys tests that the edge's conversation is
// carried as the three W3C baggage members every downstream service reads.
//
// Why this test is important:
//   - The Python services read exactly vv.conversation.id, vv.turn.index and
//     vv.prev.traceparent; a renamed or mis-encoded member silently unthreads
//     every downstream span of the conversation
//
// What it tests:
//   - ParseTraceparent round-trips a valid traceparent and rejects garbage
//   - the baggage carries the id, the decimal turn index and the previous turn's
//     traceparent, byte-for-byte
//   - an id outside ^[A-Za-z0-9_-]{1,64}$ leaves the context's baggage untouched
func TestInjectConversation_SetsBaggageKeys(t *testing.T) {
	t.Parallel()

	prev, ok := tracer.ParseTraceparent(prevTraceparent)
	require.True(t, ok)
	_, bad := tracer.ParseTraceparent("not-a-traceparent")
	assert.False(t, bad)

	ctx := tracer.InjectConversation(
		context.Background(),
		tracer.Conversation{ID: "conv_42", TurnIndex: 3, Prev: prev},
	)
	bag := baggage.FromContext(ctx)

	assert.Equal(t, "conv_42", bag.Member("vv.conversation.id").Value())
	assert.Equal(t, "3", bag.Member("vv.turn.index").Value())
	assert.Equal(t, prevTraceparent, bag.Member("vv.prev.traceparent").Value())

	untouched := tracer.InjectConversation(
		context.Background(),
		tracer.Conversation{ID: "a@b.com", TurnIndex: 1},
	)
	assert.Equal(t, 0, baggage.FromContext(untouched).Len())
}

// TestConversationStartOptions_LinksPrevTurn tests that a later turn's root span
// is stamped with the conversation attributes and linked to the previous turn.
//
// Why this test is important:
//   - Each turn is its own trace; the attributes group them and the link lets an
//     operator walk back to the previous turn
//
// What it tests:
//   - the options carry exactly conversation.id and turn.index attributes
//   - exactly one link, to the previous turn's span context
func TestConversationStartOptions_LinksPrevTurn(t *testing.T) {
	t.Parallel()

	prev, ok := tracer.ParseTraceparent(prevTraceparent)
	require.True(t, ok)

	cfg := trace.NewSpanStartConfig(
		tracer.ConversationStartOptions(
			tracer.Conversation{ID: "c1", TurnIndex: 2, Prev: prev},
		)...)

	assert.Equal(t, []attribute.KeyValue{
		attribute.String("conversation.id", "c1"),
		attribute.Int("turn.index", 2),
	}, cfg.Attributes())
	require.Len(t, cfg.Links(), 1)
	assert.Equal(t, prev, cfg.Links()[0].SpanContext)
}

// TestConversationStartOptions_FirstTurnHasNoLink tests that the first turn
// carries the attributes but no link.
//
// Why this test is important:
//   - A link to an invalid (zero) span context renders as a dangling reference in
//     the trace backend
//
// What it tests:
//   - turn 0 with no previous span: the two attributes and zero links
func TestConversationStartOptions_FirstTurnHasNoLink(t *testing.T) {
	t.Parallel()

	cfg := trace.NewSpanStartConfig(
		tracer.ConversationStartOptions(tracer.Conversation{ID: "c1", TurnIndex: 0})...)

	assert.Len(t, cfg.Attributes(), 2)
	assert.Empty(t, cfg.Links())
}

// TestConversationStartOptions_RejectsInvalidConversation tests that a malformed
// conversation stamps nothing on the root span.
//
// Why this test is important:
//   - The id often comes from an untrusted request header; InjectConversation
//     already refuses a malformed one for baggage, and the span path must refuse
//     it too, or a multi-KB or PII-shaped value lands as a span attribute
//
// What it tests:
//   - an id with an "@", a 65-character id and a negative turn index each yield
//     no options: zero attributes and zero links, even with a valid Prev
func TestConversationStartOptions_RejectsInvalidConversation(t *testing.T) {
	t.Parallel()
	prev, ok := tracer.ParseTraceparent(prevTraceparent)
	require.True(t, ok)

	for _, c := range []tracer.Conversation{
		{ID: "user@example.com", TurnIndex: 1, Prev: prev},
		{ID: strings.Repeat("a", 65), TurnIndex: 1, Prev: prev},
		{ID: "c1", TurnIndex: -1, Prev: prev},
	} {
		opts := tracer.ConversationStartOptions(c)
		assert.Empty(t, opts, "conversation %q turn %d", c.ID, c.TurnIndex)
	}
}
