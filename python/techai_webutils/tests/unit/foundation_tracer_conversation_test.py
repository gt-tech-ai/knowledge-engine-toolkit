"""Unit tests for the conversation-threading primitives (baggage → span attributes + span links)."""

from __future__ import annotations

from unittest.mock import MagicMock, call

from opentelemetry import baggage
from opentelemetry.context import Context
from opentelemetry.trace import Link, SpanContext, TraceFlags

from techai_webutils.foundation.tracer.conversation import (
    ConversationContext,
    conversation_links,
    extract_conversation,
    stamp_conversation,
)

_PREV = SpanContext(
    trace_id=0x4BF92F3577B34DA6A3CE929D0E0E4736,
    span_id=0x00F067AA0BA902B7,
    is_remote=True,
    trace_flags=TraceFlags(TraceFlags.SAMPLED),
)
_PREV_TRACEPARENT = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"


def _ctx(**members: str) -> Context:
    """Return a context carrying the given baggage members."""
    ctx = Context()
    for key, value in members.items():
        ctx = baggage.set_baggage(key.replace("_", "."), value, context=ctx)
    return ctx


def test_root_span_stamped_with_conversation_id_and_turn_index() -> None:
    """Test that a turn's baggage round-trips into ``conversation.id`` / ``turn.index`` on the root span.

    **Why this test is important:**
      - The trace explorer groups a conversation's turns by these two attributes; the edge sets
        them as baggage and every service's root span must carry them.

    **What it tests:**
      - ``extract_conversation`` reads ``vv.conversation.id``, ``vv.turn.index`` and
        ``vv.prev.traceparent`` into the exact ``ConversationContext``
      - ``stamp_conversation`` sets exactly ``conversation.id`` and ``turn.index`` (an int)
    """
    ctx = _ctx(
        vv_conversation_id="conv_42",
        vv_turn_index="3",
        vv_prev_traceparent=_PREV_TRACEPARENT,
    )
    span = MagicMock()

    conv = extract_conversation(ctx)
    assert conv is not None
    stamp_conversation(span, conv)

    assert conv == ConversationContext(
        conversation_id="conv_42", turn_index=3, prev=_PREV
    )
    assert span.set_attribute.call_args_list == [
        call("conversation.id", "conv_42"),
        call("turn.index", 3),
    ]


def test_turn_links_to_previous_turn() -> None:
    """Test that a later turn links to the previous turn's root span and turn 0 links nothing.

    **Why this test is important:**
      - Each turn is its own trace; the span link is what lets an operator walk from one turn
        to the one before it.

    **What it tests:**
      - with a ``prev`` span context, exactly one ``Link`` to it
      - turn 0 (no ``prev``) yields no links
    """
    later = ConversationContext(conversation_id="c1", turn_index=1, prev=_PREV)
    first = ConversationContext(conversation_id="c1", turn_index=0, prev=None)

    links = conversation_links(later)

    assert len(links) == 1
    assert isinstance(links[0], Link)
    assert links[0].context == _PREV
    assert conversation_links(first) == []


def test_missing_conversation_context_degrades_to_unthreaded_trace() -> None:
    """Test that a request with no conversation baggage yields no conversation (no error).

    **Why this test is important:**
      - Non-conversational callers (health checks, batch jobs) must trace exactly as before.

    **What it tests:**
      - an empty context, and one missing ``vv.turn.index``, both return ``None``
      - a malformed ``vv.prev.traceparent`` keeps the conversation and drops only the link
    """
    assert extract_conversation(Context()) is None
    assert extract_conversation(_ctx(vv_conversation_id="c1")) is None
    assert extract_conversation(
        _ctx(
            vv_conversation_id="c1",
            vv_turn_index="2",
            vv_prev_traceparent="not-a-traceparent",
        )
    ) == ConversationContext(conversation_id="c1", turn_index=2, prev=None)


def test_malformed_conversation_id_is_dropped() -> None:
    """Test that an id outside ``^[A-Za-z0-9_-]{1,64}$`` or a bad turn index drops the conversation.

    **Why this test is important:**
      - Baggage is caller-controlled; an id carrying an e-mail or 10 KB of text must never reach
        span attributes (PII and cardinality).

    **What it tests:**
      - ids with a space, an ``@``, 65 characters, or empty, and turn indexes ``-1`` / ``x`` /
        a non-ASCII digit, each return ``None``
    """
    for bad_id in ("has space", "a@b.com", "x" * 65, ""):
        assert (
            extract_conversation(_ctx(vv_conversation_id=bad_id, vv_turn_index="1"))
            is None
        )
    for bad_turn in ("-1", "x", "\u00b2"):
        assert (
            extract_conversation(_ctx(vv_conversation_id="c1", vv_turn_index=bad_turn))
            is None
        )
