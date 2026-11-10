"""In-memory ``MessagePublisher`` for no-infra builds/tests.

``InMemoryPublisher`` enqueues messages onto an ``InMemoryBroker`` instead of SQS, so a
service can build and unit-test the publish path with no ElasticMQ/SQS. Mirrors Go's
``messaging/memory``. Owns no external resource, so its context lifecycle is a no-op. A
keyed publish carries its key in the message metadata under ``PARTITION_KEY_ATTRIBUTE``; a
topic is one FIFO queue, so messages arrive in publish order, per key included.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from techai_webutils.clients.messaging.config import DEFAULT_MAX_PAYLOAD_BYTES
from techai_webutils.clients.messaging.payload import check_payload_size
from techai_webutils.core.interfaces.messaging import (
    PARTITION_KEY_ATTRIBUTE,
    KeyedPublisher,
    Message,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.tracer.propagate import inject

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.memory.broker import InMemoryBroker


@final
class InMemoryPublisher(NoOpAsyncResource, KeyedPublisher):
    """A ``KeyedPublisher`` that enqueues onto a shared ``InMemoryBroker`` (dev/test).

    Owns no external resource, so its async-context lifecycle is the shared
    ``NoOpAsyncResource``.
    """

    def __init__(
        self,
        broker: InMemoryBroker,
        *,
        max_payload_bytes: int = DEFAULT_MAX_PAYLOAD_BYTES,
    ) -> None:
        """Publish onto ``broker``.

        A subscriber built from the same broker receives the messages.

        A payload over ``max_payload_bytes`` is rejected, as the strictest real broker
        would.

        Args:
            broker: The broker the messages go to.
            max_payload_bytes: The largest payload accepted.

        """
        self._broker = broker
        self._max_payload_bytes = max_payload_bytes
        self._seq = 0

    @override
    async def publish(self, topic: str, payload: bytes) -> None:
        """Enqueue ``payload`` on ``topic`` as a ``Message``.

        With a monotonic in-memory id.

        The current trace context rides the message's metadata, so the subscriber's
        handler continues this trace.

        Args:
            topic: The destination topic or queue.
            payload: The message body.

        """
        await self._enqueue(topic, {}, payload)

    @override
    async def publish_keyed(self, topic: str, key: str, payload: bytes) -> None:
        """Enqueue ``payload`` on ``topic`` with ``key`` in its metadata.

        Args:
            topic: The destination topic or queue.
            key: The ordering key; messages with one key are delivered in order.
            payload: The message body.

        """
        await self._enqueue(topic, {PARTITION_KEY_ATTRIBUTE: key}, payload)

    @override
    async def publish_batch_keyed(
        self, topic: str, items: list[tuple[str, bytes]]
    ) -> None:
        """Enqueue each ``(key, payload)`` item on ``topic`` in order.

        Args:
            topic: The destination topic or queue.
            items: ``(key, payload)`` pairs, in order.

        """
        for key, payload in items:
            await self.publish_keyed(topic, key, payload)

    @property
    @override
    def ordered_per_key(self) -> bool:
        """``True``: a topic's messages are delivered in publish order."""
        return True

    async def _enqueue(
        self, topic: str, metadata: dict[str, str], payload: bytes
    ) -> None:
        """Put ``payload`` on ``topic`` with ``metadata`` plus the current trace context.

        A callee raises ``AppValueError``: ``payload`` is over ``max_payload_bytes``.

        Args:
            topic: The topic.
            metadata: The message's metadata.
            payload: The message's bytes.

        """
        check_payload_size(payload, self._max_payload_bytes)
        self._seq += 1
        inject(metadata)
        await self._broker.queue(topic).put(
            Message(
                id=f"mem-{self._seq}", topic=topic, payload=payload, metadata=metadata
            ),
        )

    @override
    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Enqueue each payload on ``topic`` in order.

        Args:
            topic: The destination topic or queue.
            payloads: The message bodies, in order.

        """
        for payload in payloads:
            await self.publish(topic, payload)
