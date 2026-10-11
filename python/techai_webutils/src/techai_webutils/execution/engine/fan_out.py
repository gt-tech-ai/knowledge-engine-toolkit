"""Async bounded-concurrency fan-out -- the Python analog of Go's ``engine.FanOut``.

Runs an async mapper over a batch of items with a concurrency cap, isolating each item's
failure so one bad item never aborts the batch (mirroring the Go engine's panic
isolation). Cancellation propagates: unlike a normal per-item error (captured as a
``StepResult(FAIL)``), ``asyncio.CancelledError`` is never swallowed. Results are returned
in item order, and the observer receives exactly one ``on_step_complete`` per item.
"""

from __future__ import annotations

import asyncio
import traceback
from dataclasses import replace
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.execution import (
    BatchResult,
    NopObserver,
    StepResult,
    StepStatus,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Sequence

    from techai_webutils.core.interfaces.execution import ExecutionObserver

# Fallback concurrency when the caller passes <= 0. Tuned for I/O-bound async work
# (the ingestion mapper is download + parse-offload + network), not CPU parallelism.
_DEFAULT_CONCURRENCY = 8
"""Fallback semaphore size (I/O-bound default) used when the caller passes concurrency <= 0."""


async def fan_out[T](
    items: Sequence[T],
    concurrency: int,
    fn: Callable[[T], Awaitable[StepResult]],
    observer: ExecutionObserver | None = None,
    *,
    name: str = "fanout",
) -> BatchResult:
    """Run ``fn`` over ``items`` with bounded concurrency, returning an ordered BatchResult.

    Each item runs under a semaphore of size ``max(concurrency, 1)`` (``concurrency <= 0``
    falls back to a sane I/O default). A regular exception from ``fn`` is isolated into a
    ``StepResult(FAIL)`` carrying the message + traceback and never cancels siblings;
    ``asyncio.CancelledError`` is re-raised so cancellation is not swallowed. The observer
    receives ``on_batch_start`` once, ``on_step_complete`` exactly once per item, and
    ``on_batch_complete`` once (unless the fan-out is cancelled).
    """
    obs = observer or NopObserver()
    work = list(items)
    obs.on_batch_start(name, len(work))
    loop = asyncio.get_running_loop()
    start = loop.time()

    if not work:
        empty = BatchResult()
        obs.on_batch_complete(name, empty, 0.0)
        return empty

    limit = concurrency if concurrency > 0 else _DEFAULT_CONCURRENCY
    results: list[StepResult | None] = [None] * len(work)

    # A fixed pool of `limit` workers pulls items from a shared cursor, so at most `limit` tasks (and
    # their coroutine frames) exist at once — instead of eagerly creating one task per item and letting
    # the semaphore cap only *execution*. At bulk scale (tens of thousands of objects) this
    # bounds driver memory to O(concurrency), not O(batch). Results stay index-ordered and each item is
    # still isolated + observed exactly once, so the contract is unchanged.
    next_index = 0

    async def worker() -> None:
        """Pull-and-process items until the batch is drained, isolating each item's failure."""
        nonlocal next_index
        while next_index < len(work):
            # Claim an index. No await between the read and the increment, so on the single-threaded
            # event loop this is atomic — two workers never claim the same item.
            index = next_index
            next_index += 1
            began = loop.time()
            try:
                res = await fn(work[index])
            except asyncio.CancelledError:
                raise  # propagate cancellation -- never convert it to a FAIL result
            except Exception as exc:
                res = StepResult(
                    name=f"{name}[{index}]",
                    status=StepStatus.FAIL,
                    error=str(exc),
                    detail=traceback.format_exc(),
                    duration=loop.time() - began,
                )
            else:
                if not res.duration:
                    res = replace(res, duration=loop.time() - began)
            results[index] = res
            obs.on_step_complete(res)

    workers = [asyncio.create_task(worker()) for _ in range(min(limit, len(work)))]
    outcomes = await asyncio.gather(*workers, return_exceptions=True)
    for outcome in outcomes:
        # Normal Exceptions were isolated into FAIL results inside the worker; only a
        # BaseException (CancelledError, KeyboardInterrupt, ...) can reach here -- re-raise
        # it so cancellation/interrupt is never swallowed.
        if isinstance(outcome, BaseException):
            raise outcome

    batch = BatchResult(results=tuple(r for r in results if r is not None))
    obs.on_batch_complete(name, batch, loop.time() - start)
    return batch
