"""In-memory ``MessageConsumer`` for no-infra builds/tests.

``InMemorySubscriber`` reads messages from an ``InMemoryBroker`` instead of SQS, so a worker can
build and unit-test the consume path with no ElasticMQ/SQS. Mirrors Go's ``messaging/memory``.
Owns no external resource; ``close`` stops the receive loop. Each handler call continues the
publisher's trace and runs through the handler tier. With ``DeliveryConfig.max_deliveries`` a
failing message is redelivered after its ``retry_backoff`` delay until it has been delivered that
many times, then moved to ``dead_letter_topic``; without it a failing message is dropped, as before.
``backlog`` reports the topic's queue length; the broker keeps no enqueue time, so no age.
"""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING, Self

from techai_webutils.clients.messaging.config import DeliveryConfig
from techai_webutils.clients.messaging.handling import run_handler
from techai_webutils.core.interfaces.messaging import LeaseExtender, MessageConsumer
from techai_webutils.core.interfaces.messaging_backlog import Backlog, BacklogProbe

if TYPE_CHECKING:
    from types import TracebackType

    from techai_webutils.clients.client import ClientBase
    from techai_webutils.clients.messaging.memory.broker import InMemoryBroker
    from techai_webutils.core.interfaces.messaging import Message, MessageHandler

logger = logging.getLogger(__name__)

_POLL_TIMEOUT = 0.05
"""Seconds each queue read blocks so the loop can observe ``close`` promptly without a sentinel."""


class InMemorySubscriber(MessageConsumer, LeaseExtender, BacklogProbe):
    """A ``MessageConsumer`` that reads from a shared ``InMemoryBroker`` (dev/test)."""

    def __init__(
        self,
        broker: InMemoryBroker,
        handlers: ClientBase | None = None,
        *,
        delivery: DeliveryConfig | None = None,
    ) -> None:
        """Consume from ``broker``, running each handler call through ``handlers``.

        A publisher built from the same broker feeds this subscriber. ``handlers`` is the handler
        tier (``ClientBase.wire`` with ``handler()``); None runs handlers undecorated.
        ``delivery`` sets redelivery and dead-lettering (None: a failing message is dropped).
        """
        self._broker = broker
        self._handlers = handlers
        self._delivery = delivery or DeliveryConfig()
        self._deliveries: dict[int, int] = {}
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
            # A handler failure never kills the loop: the message is redelivered or
            # dead-lettered per the delivery config, or dropped when none is set.
            try:
                await run_handler(self._handlers, topic, handler, message)
            except Exception:
                logger.debug("in-memory handler failed", exc_info=True)
                self._retry_or_dead_letter(topic, message)
            else:
                self._deliveries.pop(id(message), None)

    async def backlog(self, topic: str) -> Backlog:
        """Return the number of messages queued on ``topic``; the age is not tracked."""
        return Backlog(
            messages=self._broker.queue(topic).qsize(), oldest_age_seconds=None
        )

    async def extend(self, message: Message, seconds: float) -> None:
        """Do nothing: the in-memory broker never redelivers a message that is being handled."""

    def _retry_or_dead_letter(self, topic: str, message: Message) -> None:
        """Requeue a failed ``message`` after its backoff, or move it to the dead-letter topic.

        Deliveries are counted per message object, which a redelivery reuses. Without
        ``max_deliveries`` the message is dropped, the subscriber's behaviour before redelivery.
        """
        limit = self._delivery.max_deliveries
        if limit is None:
            return
        delivered = self._deliveries.get(id(message), 0) + 1
        if delivered < limit:
            self._deliveries[id(message)] = delivered
            message.delivery = delivered + 1
            delay = self._delivery.backoff_seconds(delivered) or 0.0
            asyncio.get_running_loop().call_later(
                delay, self._broker.queue(topic).put_nowait, message
            )
            return
        self._deliveries.pop(id(message), None)
        dead_letter = self._delivery.dead_letter_topic
        if dead_letter is None:
            logger.warning(
                "in-memory message %s dropped after %d deliveries", message.id, limit
            )
            return
        self._broker.queue(dead_letter).put_nowait(message)

    async def close(self) -> None:
        """Signal the receive loop to stop after the current message."""
        self._closed.set()
