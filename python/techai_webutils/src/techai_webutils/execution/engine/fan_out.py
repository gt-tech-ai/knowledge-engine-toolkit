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
from dataclasses import dataclass, replace
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.execution import (
    BatchResult,
    NopObserver,
    StepResult,
    StepStatus,
)
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Iterator, Sequence

    from techai_webutils.core.interfaces.execution import ExecutionObserver

logger = get_logger(__name__)

# Fallback concurrency when the caller passes <= 0. Tuned for I/O-bound async work
# (the ingestion mapper is download + parse-offload + network), not CPU parallelism.
_DEFAULT_CONCURRENCY = 8
"""Fallback semaphore size (I/O-bound default) used when the caller passes concurrency <=
0."""


@dataclass(frozen=True, kw_only=True)
class _WorkerPool[T]:
    """What ``_run_workers`` takes, grouped into one value."""

    work: list[T]
    """The ``work`` value of ``_run_workers``."""

    limit: int
    """The ``limit`` value of ``_run_workers``."""

    fn: Callable[[T], Awaitable[StepResult]]
    """The ``fn`` value of ``_run_workers``."""

    results: list[StepResult | None]
    """The ``results`` value of ``_run_workers``."""

    name: str
    """The ``name`` value of ``_run_workers``."""

    obs: ExecutionObserver
    """The ``obs`` value of ``_run_workers``."""


async def _run_workers[T](options: _WorkerPool[T]) -> None:
    """Drain ``work`` through at most ``limit`` workers, filling ``results`` in place.

    A worker isolates a regular exception into a FAIL result, so only a
    ``BaseException`` (``CancelledError``, ``KeyboardInterrupt``, ...) escapes one; once
    every worker has settled, the first such escape propagates from its task's
    ``result()``, so cancellation and interrupts are never swallowed. Cancelling this
    call cancels every worker and returns once they have all unwound.

    Args:
        options: The work, the step, the concurrency limit and where results go.

    """
    # A fixed pool of `limit` workers pulls from one shared iterator, so at most `limit`
    # coroutine frames exist at once: driver memory is O(concurrency), not O(batch).
    # Results stay index-ordered and each item is isolated and observed exactly once.
    pending = iter(enumerate(options.work))
    workers = [
        asyncio.create_task(
            _drain(pending, options.fn, options.results, options.name, options.obs)
        )
        for _ in range(min(options.limit, len(options.work)))
    ]
    if not workers:
        return
    try:
        _ = await asyncio.wait(workers)
    finally:
        # The caller's cancellation stops the workers; wait for them to unwind so none
        # outlives the call.
        for worker in workers:
            _ = worker.cancel()
        _ = await asyncio.wait(workers)
    for worker in workers:
        worker.result()


async def fan_out[T](
    items: Sequence[T],
    concurrency: int,
    fn: Callable[[T], Awaitable[StepResult]],
    observer: ExecutionObserver | None = None,
    *,
    name: str = "fanout",
) -> BatchResult:
    """Run ``fn`` over ``items`` with bounded concurrency; results keep input order.

    Each item runs under a semaphore of size ``max(concurrency, 1)`` (``concurrency <= 0``
    falls back to a sane I/O default). A regular exception from ``fn`` is isolated into a
    ``StepResult(FAIL)`` with its traceback and never cancels siblings;
    ``asyncio.CancelledError`` is re-raised so cancellation is not swallowed. The observer
    receives ``on_batch_start`` once, ``on_step_complete`` exactly once per item, and
    ``on_batch_complete`` once unless cancelled.

    Args:
        items: The work items.
        concurrency: The most items run at once.
        fn: The step applied to each item.
        observer: Observes each item's start and result; ``None`` observes nothing.
        name: The batch's name.

    Returns:
        The batch result.

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

    await _run_workers(
        options=_WorkerPool(
            work=work, limit=limit, fn=fn, results=results, name=name, obs=obs
        )
    )

    batch = BatchResult(results=tuple(r for r in results if r is not None))
    obs.on_batch_complete(name, batch, loop.time() - start)
    return batch


async def _drain[T](
    pending: Iterator[tuple[int, T]],
    fn: Callable[[T], Awaitable[StepResult]],
    results: list[StepResult | None],
    name: str,
    obs: ExecutionObserver,
) -> None:
    """Process items from the shared ``pending`` iterator until it is exhausted.

    ``next`` on the iterator never awaits, so on the single-threaded event loop two
    workers never claim the same item.

    Args:
        pending: The shared iterator of indexed items.
        fn: The step.
        results: Where each item's result goes, by index.
        name: The batch's name.
        obs: Observes each item.

    """
    for index, item in pending:
        res = await _run_one(fn, item, f"{name}[{index}]")
        results[index] = res
        obs.on_step_complete(res)


async def _run_one[T](
    fn: Callable[[T], Awaitable[StepResult]],
    item: T,
    step_name: str,
) -> StepResult:
    """Run ``fn`` on one item, isolating a regular exception into a timed FAIL result.

    ``asyncio.CancelledError`` is a ``BaseException``, not an ``Exception``, so it
    propagates: cancellation is never converted to a FAIL result.

    Args:
        fn: The step.
        item: The item.
        step_name: The result's name.

    Returns:
        The step result for the item.

    """
    loop = asyncio.get_running_loop()
    began = loop.time()
    try:
        res = await fn(item)
    except Exception as exc:
        logger.exception("run_one failed")
        return StepResult(
            name=step_name,
            status=StepStatus.FAIL,
            error=str(exc),
            detail=traceback.format_exc(),
            duration=loop.time() - began,
        )
    if not res.duration:
        res = replace(res, duration=loop.time() - began)
    return res
