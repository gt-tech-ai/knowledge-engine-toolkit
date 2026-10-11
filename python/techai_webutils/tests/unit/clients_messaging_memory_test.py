"""Tests for the in-memory messaging backend + its factory selection.

The ``memory`` publisher/subscriber let a worker build and unit-test the SQS path with no
ElasticMQ/SQS — the stub-first property (ARCHITECTURE.md#stub-first-backends). Parity with Go's ``messaging/memory``.
Publisher and subscriber connect only through a **shared broker instance** (not module-global
state), so two unrelated brokers are isolated under parallel test execution.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

import pytest

from techai_webutils.clients.messaging.builder import (
    MessagingConfig,
    MessagingKind,
    new_messaging_from_config,
    new_messaging_subscriber_from_config,
)
from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.messaging.memory import (
    InMemoryBroker,
    InMemoryPublisher,
    InMemorySubscriber,
)

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.interfaces.messaging import Message


async def _wait_for(predicate: Callable[[], bool], within_s: float = 1.0) -> None:
    """Poll ``predicate`` until truthy or ``within_s`` elapses (condition-based waiting)."""
    for _ in range(int(within_s / 0.01)):
        if predicate():
            return
        await asyncio.sleep(0.01)


@pytest.mark.asyncio
async def test_messaging_memory_backend_publish_receive() -> None:
    """A message published to a topic reaches a subscriber on the SAME broker only.

    A second broker sees nothing (isolation).

    Why this test is important:
        - The memory backend only stands in for SQS if a publish reaches a subscriber on the same
          broker, and if separate brokers do NOT leak into each other (else parallel tests sharing a
          topic name would flake). Both properties must hold.

    What it tests:
        - publish → the subscriber's handler receives the payload; a subscriber on a different
          broker receives nothing for the same topic.
    """
    broker = InMemoryBroker()
    publisher = InMemoryPublisher(broker)
    subscriber = InMemorySubscriber(broker)
    received: list[bytes] = []

    async def handler(msg: Message) -> None:
        await asyncio.sleep(0)
        received.append(msg.payload)

    await publisher.publish("topic", b"hello")
    task = asyncio.create_task(subscriber.subscribe("topic", handler))
    await _wait_for(lambda: bool(received))
    await subscriber.close()
    await task

    assert received == [b"hello"], (
        "the subscriber on the same broker receives the message"
    )

    # Isolation: a subscriber on a DIFFERENT broker sees none of the first broker's messages.
    other = InMemorySubscriber(InMemoryBroker())
    other_received: list[bytes] = []

    async def other_handler(msg: Message) -> None:
        await asyncio.sleep(0)
        other_received.append(msg.payload)

    other_task = asyncio.create_task(other.subscribe("topic", other_handler))
    await asyncio.sleep(0.05)
    await other.close()
    await other_task

    assert other_received == [], (
        "a separate broker does not see the first broker's messages"
    )


@pytest.mark.asyncio
async def test_messaging_memory_handler_error_does_not_stop_the_loop() -> None:
    """A handler that raises does not kill the subscribe loop; later messages still arrive.

    Why this test is important:
        - A single bad message must not take down the whole consumer (mirroring the Go subscriber,
          which ignores the handler's error return). The earlier ``suppress(TimeoutError)`` wrapped
          the handler call, so a handler raising ``TimeoutError`` was silently swallowed while any
          other error crashed the loop — this locks the consistent drop-and-continue behavior.

    What it tests:
        - With a handler that raises on the first message, the second message is still delivered.
    """
    broker = InMemoryBroker()
    publisher = InMemoryPublisher(broker)
    subscriber = InMemorySubscriber(broker)
    received: list[bytes] = []

    async def handler(msg: Message) -> None:
        await asyncio.sleep(0)
        if msg.payload == b"boom":
            msg_text = "handler failure"
            raise RuntimeError(msg_text)
        received.append(msg.payload)

    await publisher.publish("topic", b"boom")
    await publisher.publish("topic", b"ok")
    task = asyncio.create_task(subscriber.subscribe("topic", handler))
    await _wait_for(lambda: bool(received))
    await subscriber.close()
    await task

    assert received == [b"ok"], (
        "the loop survived the failing handler and delivered the next message"
    )


def test_messaging_factory_selects_memory() -> None:
    """The factories return the in-memory publisher/subscriber for ``MessagingKind.MEMORY``.

    Why this test is important:
        - The memory backend is opted into by a config kind; if the factory silently returned the
          SQS backend, the configured no-infra behavior would never take effect.

    What it tests:
        - ``new_messaging_from_config``/``new_messaging_subscriber_from_config`` with ``kind=MEMORY``
          return ``InMemoryPublisher``/``InMemorySubscriber``.
    """
    config = MessagingConfig(
        sqs=SQSConfig(endpoint="", region="us-east-1", queue_url=""),
        kind=MessagingKind.MEMORY,
    )
    assert isinstance(new_messaging_from_config(config), InMemoryPublisher)
    assert isinstance(new_messaging_subscriber_from_config(config), InMemorySubscriber)
