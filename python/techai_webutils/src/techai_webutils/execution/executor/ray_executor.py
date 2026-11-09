"""Distributed Executor: fan items out to a Ray cluster.

Reusing fan_out's driver machinery.

``RayExecutor`` satisfies the ``Executor`` protocol
(``core.interfaces.execution.Executor``) by delegating the driver-side concerns — bounded
concurrency, per-item isolation, exactly-once observer, and ``BatchResult`` aggregation —
to ``fan_out``, and dispatching each item to the cluster through the injected
``RayRuntime`` seam. Callers swap it for ``AsyncioExecutor`` via ``executor_from_config``
without touching mapper code. It imports no ``ray`` — only the injected runtime does — so
importing this module never pulls the optional Ray dependency.
"""

from __future__ import annotations

import math
from typing import TYPE_CHECKING

from techai_webutils.execution.engine.fan_out import fan_out
from techai_webutils.execution.executor.ray_runtime import RayUnavailableError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Sequence

    from techai_webutils.core.interfaces.execution import (
        BatchResult,
        ExecutionObserver,
        StepResult,
    )
    from techai_webutils.execution.executor.ray_runtime import RayRuntime


class RayExecutor:
    """Run a mapper over items on a Ray cluster — one Ray task per item.

    Via a RayRuntime seam.
    """

    def __init__(
        self,
        runtime: RayRuntime,
        observer: ExecutionObserver | None = None,
        *,
        name: str = "executor",
        task_num_cpus: float = 0.0,
    ) -> None:
        """Bind the Ray runtime seam plus the observer/batch name forwarded.

        To ``fan_out``.

        ``task_num_cpus`` is the CPUs each task declares (the runtime schedules tasks with
        the same value). Above ``0`` it bounds a batch's in-flight tasks by the cluster's
        slots (``width``); ``0`` (the default) keeps the caller's concurrency as is, so a
        cluster that autoscales on pending tasks still sees them.
        """
        self._runtime = runtime
        self._observer = observer
        self._name = name
        self._task_num_cpus = task_num_cpus

    async def width(self, cap: int) -> int:
        """Return how many tasks a batch keeps in flight.

        ``min(cap, floor(cluster CPUs / task CPUs))``.

        Never below 1, so a cluster scaled to zero workers still gets the pending task
        that makes its autoscaler add one. With no declared task CPU cost the cap is
        returned without asking the cluster.
        """
        if self._task_num_cpus <= 0:
            return cap
        cpus = await self._runtime.cluster_cpus()
        return max(1, min(cap, math.floor(cpus / self._task_num_cpus)))

    async def run[T](
        self,
        fn: Callable[[T], Awaitable[StepResult]],
        items: Sequence[T],
        *,
        concurrency: int,
    ) -> BatchResult:
        """Dispatch each item as a Ray task with bounded concurrency.

        Isolating per-item failure.

        ``fan_out``'s semaphore bounds how many ``submit`` coroutines (in-flight Ray
        tasks) run at once — ``width(concurrency)``, read from the cluster each batch — so
        the cluster is never flooded; per-item isolation and the exactly-once observer are
        inherited unchanged, so a RayTaskError becomes a single FAIL rather than aborting
        the batch.

        An unreachable cluster is NOT a per-item failure: the connection is established
        before the first item, and a ``RayUnavailableError`` (from that connect or from
        one mid-batch) aborts the batch — the remaining items are not dispatched and the
        error is raised to the caller, so a queue consumer redrives the batch instead of
        recording every item as failed.

        Raises:
            RayUnavailableError: When the Ray cluster cannot be reached.

        """
        await self._runtime.warm_up()
        unavailable: list[RayUnavailableError] = []

        async def dispatch(item: T) -> StepResult:
            if unavailable:
                raise unavailable[0]
            try:
                return await self._runtime.submit(fn, item)
            except RayUnavailableError as exc:
                unavailable.append(exc)
                raise

        width = await self.width(concurrency)
        batch = await fan_out(items, width, dispatch, self._observer, name=self._name)
        if unavailable:
            raise unavailable[0]
        return batch
