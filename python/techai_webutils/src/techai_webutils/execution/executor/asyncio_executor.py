"""In-process Executor backed by the async fan-out engine.

``AsyncioExecutor`` satisfies the ``Executor`` protocol
(``core.interfaces.execution.Executor``) by delegating to ``fan_out``. It is the default
executor for the per-document ingestion path; the distributed ``RayExecutor``
satisfies the same protocol so callers swap executors without touching mapper code.
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


class AsyncioExecutor:
    """Run a mapper over items in-process with bounded concurrency via ``fan_out``."""

    def __init__(
        self, observer: ExecutionObserver | None = None, *, name: str = "executor"
    ) -> None:
        """Store the observer + batch name forwarded to ``fan_out`` on each ``run``."""
        self._observer = observer
        self._name = name

    async def run[T](
        self,
        fn: Callable[[T], Awaitable[StepResult]],
        items: Sequence[T],
        *,
        concurrency: int,
    ) -> BatchResult:
        """Apply ``fn`` to each item with bounded concurrency, isolating per-item failure."""
        return await fan_out(items, concurrency, fn, self._observer, name=self._name)
