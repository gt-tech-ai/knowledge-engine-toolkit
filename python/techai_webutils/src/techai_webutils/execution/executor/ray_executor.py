"""Distributed Executor: fan items out to a Ray cluster, reusing fan_out's driver machinery.

``RayExecutor`` satisfies the ``Executor`` protocol (``core.interfaces.execution.Executor``) by
delegating the driver-side concerns — bounded concurrency, per-item isolation, exactly-once
observer, and ``BatchResult`` aggregation — to ``fan_out``, and dispatching each item to the
cluster through the injected ``RayRuntime`` seam. Callers swap it for ``AsyncioExecutor`` via
``executor_from_config`` without touching mapper code. It imports no ``ray`` — only the injected
runtime does — so importing this module never pulls the optional Ray dependency.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.execution.engine.fan_out import fan_out

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Sequence

    from techai_webutils.core.interfaces.execution import (
        BatchResult,
        ExecutionObserver,
        StepResult,
    )
    from techai_webutils.execution.executor.ray_runtime import RayRuntime


class RayExecutor:
    """Run a mapper over items on a Ray cluster — one Ray task per item — via a RayRuntime seam."""

    def __init__(
        self,
        runtime: RayRuntime,
        observer: ExecutionObserver | None = None,
        *,
        name: str = "executor",
    ) -> None:
        """Bind the Ray runtime seam plus the observer/batch name forwarded to ``fan_out``."""
        self._runtime = runtime
        self._observer = observer
        self._name = name

    async def run[T](
        self,
        fn: Callable[[T], Awaitable[StepResult]],
        items: Sequence[T],
        *,
        concurrency: int,
    ) -> BatchResult:
        """Dispatch each item as a Ray task with bounded concurrency, isolating per-item failure.

        ``fan_out``'s semaphore bounds how many ``submit`` coroutines (in-flight Ray tasks) run at
        once, so the cluster is never flooded; per-item isolation and the exactly-once observer are
        inherited unchanged, so a RayTaskError becomes a single FAIL rather than aborting the batch.
        """

        async def dispatch(item: T) -> StepResult:
            return await self._runtime.submit(fn, item)

        return await fan_out(
            items, concurrency, dispatch, self._observer, name=self._name
        )
