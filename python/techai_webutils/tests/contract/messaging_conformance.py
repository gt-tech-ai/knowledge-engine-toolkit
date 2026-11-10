"""The transport conformance suite every messaging Kind must pass.

Now and for a future Kafka Kind.

A test module builds a ``Transport`` for its Kind and parametrizes over ``CASES``; each
case is an async function taking the transport. The contract:

- at-least-once delivery;
- a failing handler is redelivered until ``max_deliveries``, then the message is
  dead-lettered;
- duplicates are possible: a message whose first handling fails is delivered again;
- per-key order holds only when the publisher's ``ordered_per_key`` says so, and keys
  round-trip;
- handlers settle independently: on a concurrent Kind a slow message never holds up
  another's completion; a serial Kind (memory) delivers in publish order instead;
- metadata, ``traceparent`` included, round-trips;
- a payload over ``max_payload_bytes`` is rejected with ``INVALID_INPUT`` before it is
  sent.

This module is imported by the Kind test modules; it is not collected on its own.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import TYPE_CHECKING

import pytest
from opentelemetry.sdk.trace import TracerProvider

from techai_webutils.clients.messaging.config import (
    DEFAULT_MAX_PAYLOAD_BYTES,
    DeliveryConfig,
)
from techai_webutils.core.errors import AppRuntimeError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.messaging import PARTITION_KEY_ATTRIBUTE

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.messaging import (
        KeyedPublisher,
        Message,
        MessageConsumer,
    )

_EXPECTED_FINISHED_COUNT = 2

_TRACER = TracerProvider().get_tracer("conformance")
_WAIT_SECONDS = 20.0
"""How long a case waits for the broker before failing (SQS redelivery takes seconds)."""


@dataclass(frozen=True)
class Transport:
    """One Kind under test: how to build its publisher and subscribers on fresh topics."""

    publisher: Callable[[int], KeyedPublisher]
    """Build a publisher enforcing the given ``max_payload_bytes``."""
    subscriber: Callable[[DeliveryConfig], MessageConsumer]
    """Build a subscriber with the given delivery settings."""
    topic: str
    """The topic every case publishes to."""
    dead_letter_topic: str
    """Where messages land after ``max_deliveries`` failures."""
    delivery: DeliveryConfig
    """Retry settings the Kind's broker is configured for (``max_deliveries`` +
    ``dead_letter_topic`` set)."""
    concurrent: bool
    """Whether the subscriber handles one receive's messages concurrently."""


async def _consume(
    transport: Transport,
    topic: str,
    delivery: DeliveryConfig,
    handler: Callable[[Message], Awaitable[None]],
    done: asyncio.Event,
) -> None:
    """Run a subscriber on ``topic`` until ``done`` is set, then close it.

    Args:
        transport: The transport under test.
        topic: The topic.
        delivery: The subscriber's delivery settings.
        handler: Handles each delivered message.
        done: Set when the subscriber should stop.

    Raises:
        TimeoutError: ``done`` was not set within ``_WAIT_SECONDS``.

    """
    subscriber = transport.subscriber(delivery)
    async with subscriber:
        consumer = asyncio.create_task(subscriber.subscribe(topic, handler))
        waiter = asyncio.create_task(done.wait())
        finished, _ = await asyncio.wait(
            {consumer, waiter}, timeout=_WAIT_SECONDS, return_when=asyncio.FIRST_COMPLETED
        )
        await subscriber.close()
        _ = waiter.cancel()
        if consumer in finished:
            consumer.result()
        await asyncio.wait_for(consumer, timeout=_WAIT_SECONDS)
    if not done.is_set():
        msg = f"conformance case timed out on topic {topic!r}"
        raise TimeoutError(msg)


async def _collect(transport: Transport, topic: str, want: int) -> list[Message]:
    """Receive ``want`` messages from ``topic`` with a plain subscriber.

    Args:
        transport: The transport under test.
        topic: The topic.
        want: How many messages to receive.

    Returns:
        The received messages.

    """
    received: list[Message] = []
    done = asyncio.Event()

    async def handler(message: Message) -> None:
        await asyncio.sleep(0)
        received.append(message)
        if len(received) >= want:
            done.set()

    await _consume(transport, topic, DeliveryConfig(), handler, done)
    return received


async def at_least_once(transport: Transport) -> None:
    """Check every published message is delivered.

    Args:
        transport: The transport under test.

    """
    payloads = [f"m{n}".encode() for n in range(12)]
    async with transport.publisher(DEFAULT_MAX_PAYLOAD_BYTES) as publisher:
        await publisher.publish_batch(transport.topic, payloads)

    received = await _collect(transport, transport.topic, len(payloads))

    assert {m.payload for m in received} == set(payloads)


async def retry_then_dead_letter(transport: Transport) -> None:
    """Check a failing message is delivered ``max_deliveries`` times, then dead-lettered.

    The failing consumer keeps running until the dead letter arrives: a broker may move
        the
    message only on the receive after its last failure (SQS does).

    Args:
        transport: The transport under test.

    """
    limit = transport.delivery.max_deliveries
    assert limit is not None
    attempts: list[bytes] = []
    dead: list[bytes] = []
    dead_lettered = asyncio.Event()

    async def failing(message: Message) -> None:
        """Record the attempt, then fail it.

        Args:
            message: The delivered message.

        Raises:
            AppRuntimeError: Always.

        """
        await asyncio.sleep(0)
        attempts.append(message.payload)
        msg = "always fails"
        raise AppRuntimeError(msg)

    async def dead_letter(message: Message) -> None:
        """Record the dead-lettered message and signal it arrived.

        Args:
            message: The delivered message.

        """
        await asyncio.sleep(0)
        dead.append(message.payload)
        dead_lettered.set()

    async with transport.publisher(DEFAULT_MAX_PAYLOAD_BYTES) as publisher:
        await publisher.publish(transport.topic, b"poison")
    _ = await asyncio.gather(
        _consume(transport, transport.topic, transport.delivery, failing, dead_lettered),
        _consume(
            transport,
            transport.dead_letter_topic,
            DeliveryConfig(),
            dead_letter,
            dead_lettered,
        ),
    )

    assert attempts == [b"poison"] * limit
    assert dead == [b"poison"]


async def duplicate_after_failure(transport: Transport) -> None:
    """Check a message whose first handling fails is delivered again (a duplicate).

    Args:
        transport: The transport under test.

    """
    seen: list[bytes] = []
    done = asyncio.Event()

    async def flaky(message: Message) -> None:
        """Fail the first delivery, then signal the redelivery arrived.

        Args:
            message: The delivered message.

        Raises:
            AppRuntimeError: On the first delivery.

        """
        await asyncio.sleep(0)
        seen.append(message.payload)
        if len(seen) == 1:
            msg = "first attempt fails"
            raise AppRuntimeError(msg)
        done.set()

    async with transport.publisher(DEFAULT_MAX_PAYLOAD_BYTES) as publisher:
        await publisher.publish(transport.topic, b"twice")
    await _consume(transport, transport.topic, transport.delivery, flaky, done)

    assert seen == [b"twice", b"twice"]


async def keys_round_trip(transport: Transport) -> None:
    """Check keys reach the consumer, and per-key order where the Kind promises it.

    Args:
        transport: The transport under test.

    """
    items = [("a", b"a1"), ("b", b"b1"), ("a", b"a2"), ("b", b"b2"), ("a", b"a3")]
    async with transport.publisher(DEFAULT_MAX_PAYLOAD_BYTES) as publisher:
        await publisher.publish_batch_keyed(transport.topic, items)
        ordered = publisher.ordered_per_key

    received = await _collect(transport, transport.topic, len(items))

    pairs = [(m.metadata[PARTITION_KEY_ATTRIBUTE], m.payload) for m in received]
    assert sorted(pairs) == sorted(items)
    if ordered:
        for key in ("a", "b"):
            assert [p for k, p in pairs if k == key] == [p for k, p in items if k == key]


async def handlers_settle_independently(transport: Transport) -> None:
    """Check a slow handler never holds up another message's completion.

    On a concurrent Kind.

    On a serial Kind the messages are handled one after the other, in publish order.

    Args:
        transport: The transport under test.

    """
    finished: list[bytes] = []
    fast_done = asyncio.Event()
    done = asyncio.Event()

    async def handler(message: Message) -> None:
        """Finish ``slow`` only after ``fast``, where the Kind runs handlers concurrently.

        Args:
            message: The delivered message.

        """
        if message.payload == b"slow" and transport.concurrent:
            _ = await asyncio.wait_for(fast_done.wait(), timeout=_WAIT_SECONDS)
        await asyncio.sleep(0)
        finished.append(message.payload)
        if message.payload == b"fast":
            fast_done.set()
        if len(finished) == _EXPECTED_FINISHED_COUNT:
            done.set()

    async with transport.publisher(DEFAULT_MAX_PAYLOAD_BYTES) as publisher:
        await publisher.publish_batch(transport.topic, [b"slow", b"fast"])
    await _consume(transport, transport.topic, DeliveryConfig(), handler, done)

    expected = [b"fast", b"slow"] if transport.concurrent else [b"slow", b"fast"]
    assert finished == expected


async def metadata_round_trips(transport: Transport) -> None:
    """Check the publisher's trace context reaches the handler in the message metadata.

    Args:
        transport: The transport under test.

    """
    async with transport.publisher(DEFAULT_MAX_PAYLOAD_BYTES) as publisher:
        with _TRACER.start_as_current_span("producer") as span:
            trace_id = f"{span.get_span_context().trace_id:032x}"
            await publisher.publish_keyed(transport.topic, "k", b"traced")

    [message] = await _collect(transport, transport.topic, 1)

    assert trace_id in message.metadata["traceparent"]
    assert message.metadata[PARTITION_KEY_ATTRIBUTE] == "k"


async def oversized_payload_is_rejected(transport: Transport) -> None:
    """Check a payload over ``max_payload_bytes`` raises ``INVALID_INPUT``.

    And is not sent.

    Args:
        transport: The transport under test.

    """
    async with transport.publisher(DEFAULT_MAX_PAYLOAD_BYTES) as publisher:
        with pytest.raises(AppValueError) as raised:
            await publisher.publish(
                transport.topic, b"x" * (DEFAULT_MAX_PAYLOAD_BYTES + 1)
            )
        assert raised.value.code is ErrorCode.INVALID_INPUT
        await publisher.publish(transport.topic, b"after")

    [message] = await _collect(transport, transport.topic, 1)

    assert message.payload == b"after"


CASES: list[Callable[[Transport], Awaitable[None]]] = [
    at_least_once,
    retry_then_dead_letter,
    duplicate_after_failure,
    keys_round_trip,
    handlers_settle_independently,
    metadata_round_trips,
    oversized_payload_is_rejected,
]
"""Every conformance case, in the order a Kind's test module runs them."""
