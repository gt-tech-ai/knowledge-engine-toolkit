"""Conditional job composition: run a wrapped AnyJob only when a predicate holds (port of conditional.go).

``when(job, predicate)`` yields a ``Conditional`` that runs ``job`` only when ``predicate()`` returns
True; otherwise it reports a single SKIP result named for the inner job, without running it.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.execution import BatchResult, StepResult, StepStatus

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.interfaces.execution import AnyJob, JobMeta


class Conditional:
    """Wraps an AnyJob and runs it only when its predicate holds (mirrors Go ``job.Conditional``)."""

    def __init__(self, inner: AnyJob, condition: Callable[[], bool]) -> None:
        """Wrap ``inner`` behind ``condition`` (evaluated at execute time)."""
        # inner is the wrapped job that runs when the condition holds.
        self._inner: AnyJob = inner
        # condition is evaluated at execute time; False short-circuits to a SKIP.
        self._condition: Callable[[], bool] = condition

    def meta(self) -> JobMeta:
        """Return the wrapped job's identity unchanged."""
        return self._inner.meta()

    async def execute(self) -> BatchResult:
        """Run the inner job when the condition holds, else return a single SKIP result."""
        if not self._condition():
            meta = self._inner.meta()
            return BatchResult(
                results=(
                    StepResult(name=meta.name, group=meta.group, status=StepStatus.SKIP),
                ),
            )
        return await self._inner.execute()


def when(job: AnyJob, condition: Callable[[], bool]) -> Conditional:
    """Wrap ``job`` with ``condition``; it runs only when the condition returns True (mirrors Go ``When``)."""
    return Conditional(job, condition)
