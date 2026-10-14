"""Tests for messaging decoration and trace propagation (Go ``messaging.Decorate``, ``tracer.Inject``).

A publish runs through the client tier under the client's name, each handler call through the
handler tier as ``<name>.handler``, and the handler continues the trace the publisher put in the
message's metadata — on the memory broker and on SQS.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest
from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider

from techai_webutils.clients.client import ClientBase
from techai_webutils.clients.messaging import (
    MessagingConfig,
    MessagingKind,
    SQSConfig,
    decorate,
    new_messaging_from_config,
    new_messaging_subscriber_from_config,
)
from techai_webutils.clients.messaging.memory import InMemoryBroker
from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.core.interfaces.messaging import MessagePublisher
from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations, Deps
from techai_webutils.foundation.tracer import continued, inject

if TYPE_CHECKING:
    from conftest import Observed

    from techai_webutils.core.interfaces.messaging import Message

_TRACER = TracerProvider().get_tracer("test")
"""A real SDK tracer, so spans carry valid, propagatable contexts."""


def _trace_id() -> str:
    """Return the current span's trace id as 32 hex digits."""
    return format(trace.get_current_span().get_span_context().trace_id, "032x")


def test_inject_and_continued_round_trip_the_trace() -> None:
    """``inject`` writes the current trace and ``continued`` resumes it.

    Why this test is important:
      - These two helpers are the whole trace hand-off across a broker.

    What it tests:
      - Inside a span the carrier gets a ``traceparent``; outside any span ``continued`` with that
        carrier makes the trace current; an empty carrier changes nothing.
    """
    carrier: dict[str, str] = {}
    with _TRACER.start_as_current_span("producer"):
        inject(carrier)
        producer = _trace_id()

    assert producer in carrier["traceparent"]
    with continued(carrier):
        assert _trace_id() == producer
    with continued({}):
        assert _trace_id() != producer
    with continued(None):
        assert _trace_id() != producer


@pytest.mark.asyncio
async def test_memory_handler_continues_the_publisher_trace(observed: Observed) -> None:
    """A memory message carries the trace; publish and handle are both decorated.

    Why this test is important:
      - An event-driven flow must stay one trace from the producer to the consumer, and both
        sides must be observed like every other client call.

    What it tests:
      - The handler sees the publisher's trace id; the publish runs as ``events.orders`` in a
        PRODUCER span and the handle as ``events.handler.orders`` in a CONSUMER span.
    """
    config = MessagingConfig(
        sqs=SQSConfig(endpoint="", region="r", queue_url=""),
        kind=MessagingKind.MEMORY,
        name="events",
    )
    decorations = Decorations(
        decoration.default_config(), Collaborators(observability=observed.observability)
    )
    broker = InMemoryBroker()
    publisher = new_messaging_from_config(config, decorations, broker=broker)
    subscriber = new_messaging_subscriber_from_config(config, decorations, broker=broker)
    seen: list[str] = []

    async def handle(_message: Message) -> None:
        await asyncio.sleep(0)
        seen.append(_trace_id())
        await subscriber.close()

    async with publisher, subscriber:
        with _TRACER.start_as_current_span("producer"):
            producer = _trace_id()
            await publisher.publish("orders", b"{}")
        await asyncio.wait_for(subscriber.subscribe("orders", handle), timeout=1)

    assert seen == [producer]
    assert [(name, kind) for name, kind, _ in observed.spans] == [
        ("events.orders", SpanKind.PRODUCER),
        ("events.handler.orders", SpanKind.CONSUMER),
    ]


@pytest.mark.asyncio
async def test_sqs_publisher_sends_the_trace_as_attributes(
    sqs_config: SQSConfig, aws_client: AsyncMock
) -> None:
    """Every SQS send carries the topic and the trace context as string attributes.

    Why this test is important:
      - SQS has no other channel for the trace; a missing attribute breaks the trace at the
        queue.

    What it tests:
      - ``publish`` and each ``publish_batch`` entry carry ``topic`` and a ``traceparent``
        holding the current trace id.
    """
    aws_client.send_message = AsyncMock()
    aws_client.send_message_batch = AsyncMock()

    async with SQSPublisher(sqs_config) as publisher:
        with _TRACER.start_as_current_span("producer"):
            producer = _trace_id()
            await publisher.publish("events", b"{}")
            await publisher.publish_batch("events", [b"a", b"b"])

    sent = aws_client.send_message.call_args.kwargs["MessageAttributes"]
    [entry, _] = aws_client.send_message_batch.call_args.kwargs["Entries"]
    for attributes in (sent, entry["MessageAttributes"]):
        assert attributes["topic"]["StringValue"] == "events"
        assert producer in attributes["traceparent"]["StringValue"]


@pytest.mark.asyncio
async def test_sqs_handler_continues_the_trace_through_the_handler_tier(
    sqs_config: SQSConfig, aws_client: AsyncMock, observed: Observed
) -> None:
    """An SQS handler runs in the publisher's trace and through the handler tier.

    Why this test is important:
      - The consumer half of the hand-off: the trace must survive the queue, and every handle
        must be observed without being retried (redelivery is the retry).

    What it tests:
      - The handler sees the trace id from the message's ``traceparent`` attribute; the handle
        runs as ``q.handler.events``; a failing handler leaves the message undeleted.
    """
    carrier: dict[str, str] = {}
    with _TRACER.start_as_current_span("producer"):
        inject(carrier)
        producer = _trace_id()
    attributes = {k: {"DataType": "String", "StringValue": v} for k, v in carrier.items()}
    aws_client.receive_message.return_value = {
        "Messages": [
            {
                "MessageId": "m",
                "Body": "{}",
                "ReceiptHandle": "r",
                "MessageAttributes": attributes,
            }
        ]
    }
    handlers = ClientBase.handler("q", Deps(observability=observed.observability))
    seen: list[str] = []

    async with SQSSubscriber(sqs_config, handlers) as subscriber:

        async def handle(_message: Message) -> None:
            await asyncio.sleep(0)
            seen.append(_trace_id())
            await subscriber.close()
            msg = "handler failed"
            raise RuntimeError(msg)

        await subscriber.subscribe("events", handle)

    assert seen == [producer]
    assert [name for name, _, _ in observed.spans] == ["q.handler.events"]
    aws_client.delete_message.assert_not_awaited()


@pytest.mark.asyncio
async def test_decorate_runs_publishes_through_the_tier_and_keeps_the_lifecycle(
    observed: Observed,
) -> None:
    """A decorated publisher observes each send by topic and opens and closes the wrapped one.

    Why this test is important:
      - The decorated publisher replaces the backend at the composition root; it must keep the
        backend's async lifecycle and send everything through it.

    What it tests:
      - Entering and leaving the decorated publisher enters and leaves the backend; ``publish``
        and ``publish_batch`` reach the backend as ``bus.<topic>`` operations.
    """
    backend = MagicMock(spec=MessagePublisher)
    backend.__aenter__ = AsyncMock(return_value=backend)
    backend.__aexit__ = AsyncMock(return_value=None)
    backend.publish = AsyncMock()
    backend.publish_batch = AsyncMock()

    async with decorate(
        backend, "bus", Deps(observability=observed.observability)
    ) as pub:
        await pub.publish("a", b"1")
        await pub.publish_batch("b", [b"2"])

    backend.__aenter__.assert_awaited_once()
    backend.__aexit__.assert_awaited_once()
    backend.publish.assert_awaited_once_with("a", b"1")
    backend.publish_batch.assert_awaited_once_with("b", [b"2"])
    assert [name for name, _, _ in observed.spans] == ["bus.a", "bus.b"]
