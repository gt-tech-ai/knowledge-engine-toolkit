"""PooledExecutor: fan items out over a fixed pool of stateful workers built once from a factory.

Where ``fan_out`` (and ``AsyncioExecutor``/``RayExecutor``) drives a *stateless* per-item mapper,
``PooledExecutor`` drives a small pool of ``PooledWorker``s that each build their (expensive) per-worker
context ONCE and reuse it across every item routed to them — per-worker client reuse, replacing the
per-object client rebuild of the stateless-task path. It reuses ``fan_out`` for the driver-side concerns
(bounded concurrency, per-item isolation, exactly-once observer, ordered ``BatchResult``) and adds only
the build-once pool + round-robin routing on top. It carries no ``ray`` import: the Ray actor pool plugs
in behind the ``WorkerFactory`` seam (``real_ray_pool.ray_worker_factory``), so importing this module
never pulls the optional Ray dependency — exactly mirroring the ``RayExecutor`` / ``RayRuntime`` /
``RealRayRuntime`` split.
"""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING, Protocol, runtime_checkable

from techai_webutils.execution.engine.fan_out import fan_out

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Sequence

    from techai_webutils.core.interfaces.execution import (
        BatchResult,
        ExecutionObserver,
        StepResult,
    )

logger = logging.getLogger(__name__)


@runtime_checkable
class PooledWorker[T](Protocol):
    """A stateful per-worker context that processes many items and is closed once at the end.

    Built once per pool slot (a Ray actor, or an in-process object) and reused across every item
    routed to it — so the expensive setup (S3 clients, parser subprocess pool) is paid per worker,
    not per item. Generic in the item type ``T`` (mirroring ``Executor.run[T]``) so a worker whose
    ``process`` takes the concrete work item (e.g. ``BulkSyncObject``) still satisfies the protocol.
    """

    async def process(self, item: T) -> StepResult:
        """Process one item on this worker's reused context, returning its StepResult.

        Like a mapper, it isolates its own failure into a ``StepResult`` where it can; an exception
        that escapes is isolated by ``fan_out`` into a single FAIL, never aborting the batch.
        """
        ...

    async def aclose(self) -> None:
        """Release the worker's resources (clients, subprocess pools) once the batch is done."""
        ...


type WorkerFactory[T] = Callable[[], Awaitable[PooledWorker[T]]]
"""Builds one ``PooledWorker[T]``; must be picklable for the Ray-actor path (module-level fn / partial).

A ``type`` alias so the RHS is evaluated lazily — ``Callable``/``Awaitable`` stay TYPE_CHECKING-only
imports (never loaded at runtime), matching the module's ray-free, import-light contract.
"""


class LazyWorker[T]:
    """Build one ``PooledWorker[T]`` from a factory on first use and reuse it — safe under concurrency.

    The build is double-checked under an ``asyncio.Lock`` so concurrent first calls build the worker
    EXACTLY once. This is the shared build-once primitive behind both pool paths: ``PooledExecutor``
    holds one per slot, and the Ray actor (``real_ray_pool._WorkerActor``) holds one per actor — where
    it is load-bearing, since an async Ray actor runs its methods concurrently on one event loop, so
    two concurrent first-item calls would otherwise each ``await factory()`` (the build yields on the
    S3-session open) and build a second worker, orphaning the first's un-closed clients. Ray-free, so
    the guarantee is unit-testable without a cluster.
    """

    def __init__(self, factory: WorkerFactory[T]) -> None:
        """Bind the (picklable) factory; the worker is built on the first ``get`` call."""
        self._factory = factory
        self._worker: PooledWorker[T] | None = None
        self._lock = asyncio.Lock()

    async def get(self) -> PooledWorker[T]:
        """Return the worker, building it exactly once (double-checked under the lock) on first call."""
        worker = self._worker
        if worker is None:
            async with self._lock:
                worker = self._worker
                if worker is None:
                    worker = await self._factory()
                    self._worker = worker
        return worker

    @property
    def built(self) -> PooledWorker[T] | None:
        """The worker if it has been built (so callers can ``aclose`` it), else ``None``."""
        return self._worker


class PooledExecutor[T]:
    """Run items over a fixed pool of ``pool_size`` workers built once each, via ``fan_out``."""

    def __init__(
        self,
        factory: WorkerFactory[T],
        *,
        pool_size: int,
        observer: ExecutionObserver | None = None,
        name: str = "executor",
    ) -> None:
        """Bind the worker factory, pool size, and the observer/batch name forwarded to ``fan_out``."""
        self._factory = factory
        self._pool_size = max(pool_size, 1)
        self._observer = observer
        self._name = name

    async def run(self, items: Sequence[T], *, concurrency: int) -> BatchResult:
        """Route each item to a pooled worker (built once per slot), isolating per-item failure.

        The pool is sized to ``min(pool_size, len(items))`` so a tiny batch never spawns idle
        workers, and every worker actually built is closed on the way out (even if ``fan_out``
        raises), so no client or actor is leaked.
        """
        size = min(self._pool_size, len(items)) or 1
        slots = [LazyWorker(self._factory) for _ in range(size)]
        cursor = 0

        async def dispatch(item: T) -> StepResult:
            nonlocal cursor
            # Claim a slot round-robin. No await between the read and the increment, so on the
            # single-threaded event loop this is atomic — concurrent dispatches spread evenly. The
            # slot's ``LazyWorker`` builds its worker exactly once even under concurrent first hits.
            slot = cursor % size
            cursor += 1
            worker = await slots[slot].get()
            return await worker.process(item)

        try:
            return await fan_out(
                items, concurrency, dispatch, self._observer, name=self._name
            )
        finally:
            # Close every built worker independently: gather so one worker's aclose failure (e.g. a Ray
            # actor proxy whose actor already died) can't strand the rest — leaking their actor slots /
            # S3 sessions is the exact resource leak this pool exists to bound. Failures are logged, not
            # raised, so cleanup never masks the fan_out result (or its in-flight exception).
            outcomes = await asyncio.gather(
                *(slot.built.aclose() for slot in slots if slot.built is not None),
                return_exceptions=True,
            )
            for outcome in outcomes:
                if isinstance(outcome, Exception):
                    logger.warning(
                        "pooled worker aclose failed: %s", outcome, exc_info=outcome
                    )
