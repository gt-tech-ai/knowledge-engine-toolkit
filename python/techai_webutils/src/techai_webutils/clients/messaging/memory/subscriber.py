"""In-memory ``MessageConsumer`` for no-infra builds/tests.

``InMemorySubscriber`` reads messages from an ``InMemoryBroker`` instead of SQS, so a worker can
build and unit-test the consume path with no ElasticMQ/SQS. Mirrors Go's ``messaging/memory``.
Owns no external resource; ``close`` stops the receive loop.
"""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING, Self

from techai_webutils.core.interfaces.messaging import MessageConsumer

if TYPE_CHECKING:
    from types import TracebackType

    from techai_webutils.clients.messaging.memory.broker import InMemoryBroker
    from techai_webutils.core.interfaces.messaging import MessageHandler

logger = logging.getLogger(__name__)

_POLL_TIMEOUT = 0.05
"""Seconds each queue read blocks so the loop can observe ``close`` promptly without a sentinel."""


class InMemorySubscriber(MessageConsumer):
    """A ``MessageConsumer`` that reads from a shared ``InMemoryBroker`` (dev/test)."""

    def __init__(self, broker: InMemoryBroker) -> None:
        """Consume from ``broker``; a publisher built from the same broker feeds this subscriber."""
        self._broker = broker
        self._closed = asyncio.Event()

    async def __aenter__(self) -> Self:
        """No resource to acquire; return self."""
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Stop the receive loop on context exit."""
        await self.close()

    async def subscribe(self, topic: str, handler: MessageHandler) -> None:
        """Deliver each message on ``topic`` to ``handler`` until ``close`` is called."""
        queue = self._broker.queue(topic)
        while not self._closed.is_set():
            # The poll timeout is the periodic wake that lets the loop observe ``close``; it is a
            # normal control signal, not a message, so it is NOT passed to the handler.
            try:
                message = await asyncio.wait_for(queue.get(), timeout=_POLL_TIMEOUT)
            except TimeoutError:
                continue
            # A handler failure is dropped so one bad message does not kill the loop, mirroring the
            # Go subscriber (which ignores the handler's error return). The in-memory broker has no
            # redelivery, so this is a stub's best effort, not at-least-once.
            try:
                await handler(message)
            except Exception:
                # Best-effort stub: log and keep consuming past a bad message (the in-memory broker
                # has no redelivery), mirroring the Go subscriber's ignore-the-error semantics.
                logger.debug(
                    "in-memory subscriber dropped a failing message", exc_info=True
                )

    async def close(self) -> None:
        """Signal the receive loop to stop after the current message."""
        self._closed.set()
