"""In-memory ``MessagePublisher`` for no-infra builds/tests.

``InMemoryPublisher`` enqueues messages onto an ``InMemoryBroker`` instead of SQS, so a service
can build and unit-test the publish path with no ElasticMQ/SQS. Mirrors Go's
``messaging/memory``. Owns no external resource, so its context lifecycle is a no-op.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.messaging import Message, MessagePublisher
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.memory.broker import InMemoryBroker


class InMemoryPublisher(NoOpAsyncResource, MessagePublisher):
    """A ``MessagePublisher`` that enqueues onto a shared ``InMemoryBroker`` (dev/test).

    Owns no external resource, so its async-context lifecycle is the shared ``NoOpAsyncResource``.
    """

    def __init__(self, broker: InMemoryBroker) -> None:
        """Publish onto ``broker``; a subscriber built from the same broker receives the messages."""
        self._broker = broker
        self._seq = 0

    async def publish(self, topic: str, payload: bytes) -> None:
        """Enqueue ``payload`` on ``topic`` as a ``Message`` with a monotonic in-memory id."""
        self._seq += 1
        await self._broker.queue(topic).put(
            Message(id=f"mem-{self._seq}", topic=topic, payload=payload),
        )

    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Enqueue each payload on ``topic`` in order."""
        for payload in payloads:
            await self.publish(topic, payload)
