"""``CoalescingEmbedding`` — pack concurrent callers' texts into shared embedder batches.

An embedding stage handles many documents at once, each with a few chunks, while an
embedding endpoint is cheapest per text at its full batch size. This decorator queues
every caller's texts (per input type) and sends them to the inner provider in batches of
``max_batch``: a batch goes as soon as it is full, and a partial one after
``linger_seconds`` from its first text, so a lone caller waits at most that long. Each
caller gets its own texts' results, in order; a failed batch fails every caller with a
text in it (each retries its own texts). Batches run concurrently; bound them with a
decorator inside this one (a bulkhead or an adaptive limit).
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.embedding import EmbeddingProvider
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.embedding import EmbeddingResult, InputType

_Item = tuple[str, "asyncio.Future[EmbeddingResult]"]
"""One queued text and the future its caller awaits."""


class CoalescingEmbedding(DelegatingAsyncResource[EmbeddingProvider], EmbeddingProvider):
    """Sends concurrent callers' texts to the inner provider in shared batches."""

    def __init__(
        self, inner: EmbeddingProvider, *, max_batch: int, linger_seconds: float
    ) -> None:
        """Batch up to ``max_batch`` texts.

        Waiting at most ``linger_seconds`` to fill one.

        Raises:
            AppValueError: ``max_batch`` below 1 or a negative ``linger_seconds``
                (``INVALID_INPUT``).

        """
        if max_batch < 1 or linger_seconds < 0:
            msg = (
                f"coalescing embedder needs max_batch >= 1 and linger >= 0, got "
                f"{max_batch}, {linger_seconds}"
            )
            raise AppValueError(msg)
        self._inner = inner
        self._max = max_batch
        self._linger = linger_seconds
        self._pending: dict[InputType, list[_Item]] = {}
        self._timers: dict[InputType, asyncio.TimerHandle] = {}
        self._batches: set[asyncio.Task[None]] = set()

    @override
    async def embed(
        self, text: str, *, input_type: InputType = "document"
    ) -> EmbeddingResult:
        """Embed ``text`` in the next shared batch."""
        (result,) = await self.embed_batch([text], input_type=input_type)
        return result

    @override
    async def embed_batch(
        self, texts: list[str], *, input_type: InputType = "document"
    ) -> list[EmbeddingResult]:
        """Queue ``texts`` into shared batches and return their results, in order."""
        if not texts:
            return []
        loop = asyncio.get_running_loop()
        items: list[_Item] = [(text, loop.create_future()) for text in texts]
        self._enqueue(input_type, items)
        return list(await asyncio.gather(*(future for _, future in items)))

    @override
    def dimension(self) -> int:
        """Return the inner provider's dimension."""
        return self._inner.dimension()

    @override
    def model_name(self) -> str:
        """Return the inner provider's model."""
        return self._inner.model_name()

    def _enqueue(self, input_type: InputType, items: list[_Item]) -> None:
        """Add ``items``, send every full batch, and time the partial one."""
        pending = self._pending.setdefault(input_type, [])
        pending.extend(items)
        while len(pending) >= self._max:
            self._send(input_type, pending[: self._max])
            del pending[: self._max]
        timer = self._timers.get(input_type)
        if not pending and timer is not None:
            timer.cancel()
            del self._timers[input_type]
        elif pending and timer is None:
            self._timers[input_type] = asyncio.get_running_loop().call_later(
                self._linger, self._expire, input_type
            )

    def _expire(self, input_type: InputType) -> None:
        """Send the partial batch whose linger ran out."""
        self._timers.pop(input_type, None)
        pending = self._pending.get(input_type, [])
        if pending:
            self._send(input_type, list(pending))
            pending.clear()

    def _send(self, input_type: InputType, items: list[_Item]) -> None:
        """Start one batch, keeping a reference until it finishes."""
        task = asyncio.get_running_loop().create_task(self._run(input_type, items))
        self._batches.add(task)
        task.add_done_callback(self._batches.discard)

    async def _run(self, input_type: InputType, items: list[_Item]) -> None:
        """Embed one batch and resolve its callers' futures.

        (a caller that left is skipped).
        """
        try:
            results = await self._inner.embed_batch(
                [text for text, _ in items], input_type=input_type
            )
        except Exception as exc:
            for _, future in items:
                if not future.done():
                    future.set_exception(exc)
            return
        for (_, future), result in zip(items, results, strict=True):
            if not future.done():
                future.set_result(result)
