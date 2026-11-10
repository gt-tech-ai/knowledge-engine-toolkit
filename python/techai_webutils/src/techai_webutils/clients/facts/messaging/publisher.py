"""``MessagingFactPublisher``: bounded, batching, fail-open fact delivery.

Facts go to a message queue.

``publish`` serializes each fact and enqueues it on an
``asyncio.Queue(maxsize=max_buffer)`` without waiting; a full queue drops the fact and
counts ``gen_ai_fact_dropped_total{reason="buffer_full"}``. Inside the async context a
sender task takes up to ten facts at a time (waiting at most ``flush_interval_s`` to fill
a batch) and sends them with one ``publish_batch`` — one SQS ``SendMessageBatch``. The
injected ``MessagePublisher`` carries its own retry, so this layer does not retry: a
failed batch is dropped and counted (``reason="publish_error"``). Exiting the context
drains the buffer within ``drain_timeout_s``; facts still buffered after it are counted
(``reason="shutdown"``). Once closing — after ``aclose`` begins, or after the sender task
died unexpectedly (logged with its exception) — ``publish`` drops and counts every fact
(``reason="closed"``) instead of buffering it.
"""

from __future__ import annotations

import asyncio
import contextlib
from dataclasses import dataclass
from typing import TYPE_CHECKING, Self, final, override

from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Sequence
    from types import TracebackType

    from techai_webutils.core.interfaces.messaging import MessagePublisher
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.types.fact import Fact

BATCH_SIZE = 10
"""Most facts per ``publish_batch`` call (the SQS ``SendMessageBatch`` limit)."""


@dataclass(frozen=True, kw_only=True)
class MessagingFactPublisherOptions:
    """What ``MessagingFactPublisher`` takes, grouped into one value."""

    max_buffer: int
    """Most facts held in memory before new ones are dropped."""

    flush_interval_s: float
    """Longest wait to fill a batch once its first fact arrived."""

    drain_timeout_s: float
    """Longest wait on context exit for buffered facts to be sent."""

    metrics: MetricsProvider | None = None
    """Provides ``gen_ai_fact_dropped_total{reason}``; ``None`` counts nothing."""


@final
class MessagingFactPublisher(FactPublisher):
    """Delivers facts through a ``MessagePublisher`` from a bounded in-process buffer."""

    def __init__(
        self,
        publisher: MessagePublisher,
        queue: str,
        options: MessagingFactPublisherOptions,
    ) -> None:
        """Bind the transport, the queue, the buffer bounds and the drop counter.

        Args:
            publisher: The message transport (its lifecycle is owned by the composition
                root).
            queue: The topic/queue name passed to ``publish_batch``.
            options: The settings and collaborators, grouped.

        """
        self._publisher = publisher
        self._queue_name = queue
        self._flush_interval_s = options.flush_interval_s
        self._drain_timeout_s = options.drain_timeout_s
        self._buffer: asyncio.Queue[bytes] = asyncio.Queue(maxsize=options.max_buffer)
        self._closing = asyncio.Event()
        self._sender: asyncio.Task[None] | None = None
        self._logger = get_logger("fact_publisher")
        self._dropped: MetricCounter | None = None
        if options.metrics is not None:
            self._dropped = options.metrics.counter(
                "gen_ai_fact_dropped_total",
                "Analytics facts dropped before publish, by reason.",
                ["reason"],
            )

    def _drop(self, count: int, reason: str) -> None:
        """Count ``count`` dropped facts under ``reason``.

        ``inc()`` counts one, matching the counter idiom.

        Args:
            count: How many facts were dropped.
            reason: Why.

        """
        if self._dropped is None:
            return
        if count == 1:
            self._dropped.inc(reason=reason)
        else:
            self._dropped.inc(count, reason=reason)

    @override
    def publish(self, facts: Sequence[Fact]) -> None:
        """Enqueue each fact's JSON without waiting.

        A full buffer drops and counts the fact.

        A closing publisher (``aclose`` begun, or its sender died) drops and counts every
        fact.

        Args:
            facts: The analytics facts to deliver; a failure to deliver is logged, never
                raised.

        """
        if self._closing.is_set():
            if facts:
                self._drop(len(facts), "closed")
            return
        for fact in facts:
            payload = fact.to_json()
            try:
                self._buffer.put_nowait(payload)
            except asyncio.QueueFull:
                self._drop(1, "buffer_full")

    @override
    async def __aenter__(self) -> Self:
        """Start the sender task.

        Returns:
            The sender task.

        """
        self._closing.clear()
        self._sender = asyncio.create_task(self._run())
        self._sender.add_done_callback(self._on_sender_done)
        return self

    def _on_sender_done(self, task: asyncio.Task[None]) -> None:
        """Log a sender that died with an exception and close the publisher.

        The failure is never silent.

        Args:
            task: The finished sender task.

        """
        if task.cancelled():
            return
        exc = task.exception()
        if exc is None:
            return
        self._closing.set()
        self._logger.error(
            "fact sender stopped; later facts are dropped",
            queue=self._queue_name,
            exc_info=exc,
        )

    @override
    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Drain the buffer within ``drain_timeout_s``, then stop the sender.

        Args:
            exc_type: The exception's type, if any.
            exc_val: The exception, if any.
            exc_tb: Its traceback, if any.

        """
        await self.aclose()

    async def aclose(self) -> None:
        """Drain buffered facts within ``drain_timeout_s`` and stop the sender.

        Facts left over are counted.

        A sender that already died is not awaited again (its exception was logged when it
        died).
        """
        self._closing.set()
        sender, self._sender = self._sender, None
        if sender is None:
            return
        if sender.done():
            # The sender died (its done-callback logged why); nothing will drain the
            # buffer.
            if self._buffer.qsize():
                self._drop(self._buffer.qsize(), "shutdown")
            return
        try:
            await asyncio.wait_for(self._buffer.join(), timeout=self._drain_timeout_s)
        except TimeoutError:
            self._drop(self._buffer.qsize(), "shutdown")
        _ = sender.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await sender

    async def _next_batch(self) -> list[bytes]:
        """Wait for one fact, then gather up to ``BATCH_SIZE``.

        It gathers for at most ``flush_interval_s``.

        Returns:
            The next batch of facts; never empty.

        """
        batch = [await self._buffer.get()]
        loop = asyncio.get_running_loop()
        deadline = loop.time() + self._flush_interval_s
        while len(batch) < BATCH_SIZE:
            try:
                batch.append(self._buffer.get_nowait())
                continue
            except asyncio.QueueEmpty:
                pass
            remaining = deadline - loop.time()
            if self._closing.is_set() or remaining <= 0:
                break
            try:
                batch.append(
                    await asyncio.wait_for(self._buffer.get(), timeout=remaining)
                )
            except TimeoutError:
                break
        return batch

    async def _run(self) -> None:
        """Send batches until cancelled.

        A failed batch is dropped and counted, never retried here.
        """
        while True:
            batch = await self._next_batch()
            try:
                await self._publisher.publish_batch(self._queue_name, batch)
            except Exception:
                self._drop(len(batch), "publish_error")
                self._logger.warning(
                    "fact batch publish failed",
                    queue=self._queue_name,
                    size=len(batch),
                    exc_info=True,
                )
            finally:
                for _ in batch:
                    self._buffer.task_done()
