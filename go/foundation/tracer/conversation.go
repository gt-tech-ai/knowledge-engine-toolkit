package tracer

import (
	"context"
	"regexp"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Conversation threading: one trace per turn, grouped by an opaque conversation
// id and linked to the previous turn's root span. The edge that owns the
// conversation calls InjectConversation so every downstream service receives the
// W3C baggage members below, and starts the turn's root span with
// ConversationStartOptions.
const (
	// BaggageConversationID is the baggage member carrying the conversation id.
	BaggageConversationID = "vv.conversation.id"
	// BaggageTurnIndex is the baggage member carrying the 0-based turn index.
	BaggageTurnIndex = "vv.turn.index"
	// BaggagePrevTraceparent is the baggage member carrying the previous turn's
	// root span as a W3C traceparent.
	BaggagePrevTraceparent = "vv.prev.traceparent"
	// AttrConversationID is the span attribute stamped with the conversation id.
	AttrConversationID = "conversation.id"
	// AttrTurnIndex is the span attribute stamped with the turn index.
	AttrTurnIndex = "turn.index"
)

// conversationIDPattern bounds a conversation id: opaque, at most 64 characters,
// no PII-shaped characters (an e-mail or free text never reaches a span).
var conversationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Conversation identifies the conversation turn a request belongs to.
type Conversation struct {
	// ID is the opaque conversation id (^[A-Za-z0-9_-]{1,64}$).
	ID string
	// Prev is the previous turn's root span; the zero value on the first turn.
	Prev trace.SpanContext
	// TurnIndex is the turn's 0-based position in the conversation.
	TurnIndex int
}

// ParseTraceparent returns the span context a W3C traceparent encodes, and
// false when the value is malformed.
func ParseTraceparent(s string) (trace.SpanContext, bool) {
	ctx := propagation.TraceContext{}.Extract(
		context.Background(),
		propagation.MapCarrier{"traceparent": s},
	)
	sc := trace.SpanContextFromContext(ctx)
	return sc, sc.IsValid()
}

// InjectConversation returns ctx with the conversation's baggage members set
// (the previous turn's traceparent only when Prev is valid). A conversation whose
// ID fails the id pattern, or a negative TurnIndex, leaves ctx unchanged, so a
// malformed id never propagates.
func InjectConversation(ctx context.Context, c Conversation) context.Context {
	if !conversationIDPattern.MatchString(c.ID) || c.TurnIndex < 0 {
		return ctx
	}
	values := map[string]string{
		BaggageConversationID: c.ID,
		BaggageTurnIndex:      strconv.Itoa(c.TurnIndex),
	}
	if c.Prev.IsValid() {
		carrier := propagation.MapCarrier{}
		propagation.TraceContext{}.Inject(
			trace.ContextWithRemoteSpanContext(context.Background(), c.Prev),
			carrier,
		)
		values[BaggagePrevTraceparent] = carrier.Get("traceparent")
	}
	bag := baggage.FromContext(ctx)
	for key, value := range values {
		member, err := baggage.NewMemberRaw(key, value)
		if err != nil {
			return ctx
		}
		if bag, err = bag.SetMember(member); err != nil {
			return ctx
		}
	}
	return baggage.ContextWithBaggage(ctx, bag)
}

// ConversationStartOptions returns the span start options for a turn's root
// span: the conversation.id and turn.index attributes, plus one link to the
// previous turn's root span when Prev is valid. A conversation InjectConversation
// would refuse (an ID failing the id pattern, or a negative TurnIndex) yields no
// options, so a malformed id never lands on a span.
func ConversationStartOptions(c Conversation) []trace.SpanStartOption {
	if !conversationIDPattern.MatchString(c.ID) || c.TurnIndex < 0 {
		return nil
	}
	opts := []trace.SpanStartOption{trace.WithAttributes(
		attribute.String(AttrConversationID, c.ID),
		attribute.Int(AttrTurnIndex, c.TurnIndex),
	)}
	if c.Prev.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: c.Prev}))
	}
	return opts
}
