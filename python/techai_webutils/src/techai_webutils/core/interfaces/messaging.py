"""Messaging interfaces for event publishing and consumption.

Mirrors Go's ``interfaces.MessagePublisher``, ``KeyedPublisher``, ``MessageConsumer``,
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
    """Broker-assigned message identifier, used to ack/delete the message after
    handling."""
    topic: str
    """The queue/topic the message was received from."""
    payload: bytes
    """The raw message body, deserialized by the handler."""
    metadata: dict[str, str] = field(default_factory=dict)
    """Message attributes carried alongside the payload (headers, correlation ids, and
    similar)."""
    timestamp: int = 0
    """Unix epoch time the message was published (0 when the broker reports none)."""
    delivery: int = 1
    """Which delivery of the message this is (1 = first), when the subscriber counts them
    (SQS: ``ApproximateReceiveCount``, read when ``DeliveryConfig.retry_backoff`` is set;
    memory: its own redelivery count); 1 otherwise."""


MessageHandler = Callable[[Message], Awaitable[None]]
"""Processes a single message. Raise to nack."""


class MessagePublisher(ManagedResource, ABC):
    """Publishes messages to a broker (SQS, etc.).

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): the SQS backend
    owns an async client lifecycle, so ``new_messaging_from_config`` returns an async
    context manager.
    """

    @abstractmethod
    async def publish(self, topic: str, payload: bytes) -> None:
        """Send a message to the specified topic."""
        ...

    @abstractmethod
    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Send multiple messages to the specified topic."""
        ...


PARTITION_KEY_ATTRIBUTE = "partition_key"
"""The attribute (SQS) or metadata key (memory) a ``KeyedPublisher`` carries a message's
key in."""


class KeyedPublisher(MessagePublisher, ABC):
    """A ``MessagePublisher`` that also attaches a partition key to each message.

    An opt-in capability (ARCHITECTURE.md#interface-composition), mirroring Go's
    ``interfaces.KeyedPublisher``: a broker that partitions by key (Kafka, Kinesis, SQS
    FIFO groups) orders messages per key, while SQS standard queues only carry the key as
    the ``partition_key`` attribute so a consumer can still read it. Redis Pub/Sub carries
    no attributes and does not implement it.
    """

    @abstractmethod
    async def publish_keyed(self, topic: str, key: str, payload: bytes) -> None:
        """Send ``payload`` to ``topic`` under ``key``."""
        ...

    @abstractmethod
    async def publish_batch_keyed(
        self, topic: str, items: list[tuple[str, bytes]]
    ) -> None:
        """Send each ``(key, payload)`` item to ``topic`` under its own key, in order."""
        ...

    @property
    @abstractmethod
    def ordered_per_key(self) -> bool:
        """Whether messages that share a key are delivered in publish order.

        A consumer that needs per-key order checks this at startup rather than assuming
        it.
        """
        ...


class LeaseExtender(ABC):
    """A consumer capability: extend the lease on a message still being handled.

    A long handler calls ``extend`` on a heartbeat so the broker does not redeliver the
    message while it is in progress. SQS maps it to ``ChangeMessageVisibility``; a Kafka
    Kind would keep the poll loop inside ``max.poll.interval.ms``.
    """

    @abstractmethod
    async def extend(self, message: Message, seconds: float) -> None:
        """Keep ``message`` leased to this consumer for ``seconds`` more from now."""
        ...


class MessageConsumer(ManagedResource, ABC):
    """Consumes messages from a queue/topic.

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): the SQS backend
    owns an async client lifecycle, so ``new_messaging_subscriber_from_config`` returns an
    async context manager.
    """

    @abstractmethod
    async def subscribe(self, topic: str, handler: MessageHandler) -> None:
        """Start consuming messages from the specified topic."""
        ...

    @abstractmethod
    async def close(self) -> None:
        """Stop the consumer gracefully."""
        ...
