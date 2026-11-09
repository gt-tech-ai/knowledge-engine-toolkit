"""In-memory ``MessagePublisher`` for no-infra builds/tests.

``InMemoryPublisher`` enqueues messages onto an ``InMemoryBroker`` instead of SQS, so a
service can build and unit-test the publish path with no ElasticMQ/SQS. Mirrors Go's
``messaging/memory``. Owns no external resource, so its context lifecycle is a no-op. A
keyed publish carries its key in the message metadata under ``PARTITION_KEY_ATTRIBUTE``; a
topic is one FIFO queue, so messages arrive in publish order, per key included.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

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
        """
        self._broker = broker
        self._max_payload_bytes = max_payload_bytes
        self._seq = 0

    async def publish(self, topic: str, payload: bytes) -> None:
        """Enqueue ``payload`` on ``topic`` as a ``Message``.

        With a monotonic in-memory id.

        The current trace context rides the message's metadata, so the subscriber's
        handler continues this trace.
        """
        await self._enqueue(topic, {}, payload)

    async def publish_keyed(self, topic: str, key: str, payload: bytes) -> None:
        """Enqueue ``payload`` on ``topic`` with ``key`` in its metadata."""
        await self._enqueue(topic, {PARTITION_KEY_ATTRIBUTE: key}, payload)

    async def publish_batch_keyed(
        self, topic: str, items: list[tuple[str, bytes]]
    ) -> None:
        """Enqueue each ``(key, payload)`` item on ``topic`` in order."""
        for key, payload in items:
            await self.publish_keyed(topic, key, payload)

    @property
    def ordered_per_key(self) -> bool:
        """``True``: a topic's messages are delivered in publish order."""
        return True

    async def _enqueue(
        self, topic: str, metadata: dict[str, str], payload: bytes
    ) -> None:
        """Put ``payload`` on ``topic`` with ``metadata`` plus the current trace context.

        Raises:
            AppValueError: ``payload`` is over ``max_payload_bytes``.

        """
        check_payload_size(payload, self._max_payload_bytes)
        self._seq += 1
        inject(metadata)
        await self._broker.queue(topic).put(
            Message(
                id=f"mem-{self._seq}", topic=topic, payload=payload, metadata=metadata
            ),
        )

    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Enqueue each payload on ``topic`` in order."""
        for payload in payloads:
            await self.publish(topic, payload)
