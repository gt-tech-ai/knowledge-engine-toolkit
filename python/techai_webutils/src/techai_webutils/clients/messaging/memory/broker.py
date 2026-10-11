"""In-process message broker shared by a memory publisher + subscriber pair.

``InMemoryBroker`` holds one ``asyncio.Queue`` per topic. It is a plain object, NOT module-global
state: a publisher and subscriber connect only when handed the SAME broker instance, so two
unrelated brokers (e.g. in parallel tests) are fully isolated.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.messaging import Message


class InMemoryBroker:
    """A process-local broker: a lazily-created ``asyncio.Queue`` per topic."""

    def __init__(self) -> None:
        """Start with no topic queues."""
        self._queues: dict[str, asyncio.Queue[Message]] = {}

    def queue(self, topic: str) -> asyncio.Queue[Message]:
        """Return the queue for ``topic``, creating it on first use."""
        existing = self._queues.get(topic)
        if existing is not None:
            return existing
        created: asyncio.Queue[Message] = asyncio.Queue()
        self._queues[topic] = created
        return created
