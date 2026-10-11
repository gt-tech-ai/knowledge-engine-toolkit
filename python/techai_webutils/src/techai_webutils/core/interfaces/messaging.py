"""Messaging interfaces for event publishing and consumption.

Mirrors Go's ``interfaces.MessagePublisher``, ``MessageConsumer``,
``MessageHandler``, and ``Message``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass
class Message:
    """A message received from a queue."""

    id: str
    """Broker-assigned message identifier, used to ack/delete the message after handling."""
    topic: str
    """The queue/topic the message was received from."""
    payload: bytes
    """The raw message body, deserialized by the handler."""
    metadata: dict[str, str] = field(default_factory=dict)
    """Message attributes carried alongside the payload (headers, correlation ids, and similar)."""
    timestamp: int = 0
    """Unix epoch time the message was published (0 when the broker reports none)."""


MessageHandler = Callable[[Message], Awaitable[None]]
"""Processes a single message. Raise to nack."""


class MessagePublisher(ManagedResource, ABC):
    """Publishes messages to a broker (SQS, etc.).

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): the SQS backend owns an async client
    lifecycle, so ``new_messaging_from_config`` returns an async context manager.
    """

    @abstractmethod
    async def publish(self, topic: str, payload: bytes) -> None:
        """Send a message to the specified topic."""
        ...

    @abstractmethod
    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Send multiple messages to the specified topic."""
        ...


class MessageConsumer(ManagedResource, ABC):
    """Consumes messages from a queue/topic.

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): the SQS backend owns an async client
    lifecycle, so ``new_messaging_subscriber_from_config`` returns an async context manager.
    """

    @abstractmethod
    async def subscribe(self, topic: str, handler: MessageHandler) -> None:
        """Start consuming messages from the specified topic."""
        ...

    @abstractmethod
    async def close(self) -> None:
        """Stop the consumer gracefully."""
        ...
