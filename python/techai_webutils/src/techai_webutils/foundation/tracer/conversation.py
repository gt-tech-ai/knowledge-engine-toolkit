"""Conversation threading: one trace per turn, linked to the previous turn and grouped by id.

The edge that owns a conversation puts three W3C baggage members on each turn's request; every
service reads them back, stamps its root span and links it to the previous turn's root span:

- baggage ``vv.conversation.id`` — an opaque id matching ``^[A-Za-z0-9_-]{1,64}$`` (never PII);
- baggage ``vv.turn.index`` — the turn's 0-based position in the conversation;
- baggage ``vv.prev.traceparent`` — the previous turn's root span as a W3C ``traceparent``.

Span attributes ``conversation.id`` and ``turn.index``; one span link to the previous root. A
missing or malformed id or index yields no conversation (an unthreaded trace); a malformed
``traceparent`` keeps the conversation and drops only the link.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import TYPE_CHECKING

from opentelemetry import baggage, trace
from opentelemetry.trace import Link
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

if TYPE_CHECKING:
    from opentelemetry.context import Context
    from opentelemetry.trace import Span, SpanContext

BAGGAGE_CONVERSATION_ID = "vv.conversation.id"
"""Baggage key carrying the conversation id."""
BAGGAGE_TURN_INDEX = "vv.turn.index"
"""Baggage key carrying the 0-based turn index."""
BAGGAGE_PREV_TRACEPARENT = "vv.prev.traceparent"
"""Baggage key carrying the previous turn's root span as a W3C ``traceparent``."""

ATTR_CONVERSATION_ID = "conversation.id"
"""Span attribute stamped with the conversation id."""
ATTR_TURN_INDEX = "turn.index"
"""Span attribute stamped with the turn index."""

_CONVERSATION_ID = re.compile(r"[A-Za-z0-9_-]{1,64}")
"""An acceptable conversation id: opaque, bounded, no PII-shaped characters."""

_TURN_INDEX = re.compile(r"[0-9]{1,9}")
"""An acceptable turn index: ASCII digits only (``str.isdigit`` would admit ``²``), bounded."""

_PROPAGATOR = TraceContextTextMapPropagator()
"""Parses a ``traceparent`` value back into a span context."""


@dataclass(frozen=True, slots=True)
class ConversationContext:
    """The conversation a request belongs to, read from its baggage."""

    conversation_id: str
    """Opaque conversation id (``^[A-Za-z0-9_-]{1,64}$``)."""
    turn_index: int
    """0-based position of this turn in the conversation."""
    prev: SpanContext | None
    """The previous turn's root span, or ``None`` on the first turn."""


def _parse_traceparent(value: str) -> SpanContext | None:
    """Return the span context a W3C ``traceparent`` encodes, or ``None`` when it is malformed."""
    ctx = _PROPAGATOR.extract({"traceparent": value})
    span_context = trace.get_current_span(ctx).get_span_context()
    return span_context if span_context.is_valid else None


def extract_conversation(ctx: Context | None = None) -> ConversationContext | None:
    """Read the conversation from ``ctx``'s baggage (the current context when ``None``).

    Returns ``None`` when the id or the turn index is missing or malformed.
    """
    conversation_id = baggage.get_baggage(BAGGAGE_CONVERSATION_ID, ctx)
    raw_turn = baggage.get_baggage(BAGGAGE_TURN_INDEX, ctx)
    if not isinstance(conversation_id, str) or not _CONVERSATION_ID.fullmatch(
        conversation_id
    ):
        return None
    if not isinstance(raw_turn, str) or not _TURN_INDEX.fullmatch(raw_turn):
        return None
    prev_raw = baggage.get_baggage(BAGGAGE_PREV_TRACEPARENT, ctx)
    prev = _parse_traceparent(prev_raw) if isinstance(prev_raw, str) else None
    return ConversationContext(
        conversation_id=conversation_id, turn_index=int(raw_turn), prev=prev
    )


def stamp_conversation(span: Span, conv: ConversationContext) -> None:
    """Set ``conversation.id`` and ``turn.index`` on ``span`` (a turn's root span)."""
    span.set_attribute(ATTR_CONVERSATION_ID, conv.conversation_id)
    span.set_attribute(ATTR_TURN_INDEX, conv.turn_index)


def conversation_links(conv: ConversationContext) -> list[Link]:
    """Return the span links for a turn's root span: one to the previous turn, none on the first."""
    return [] if conv.prev is None else [Link(conv.prev)]
