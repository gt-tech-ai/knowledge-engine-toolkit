"""The execution-job tier (mirrors Go ``execution/job`` ``Tier`` / ``Base``).

``tier()`` orders the tier's layers; ``JobBase`` runs a job's ``execute`` through it, switched and
tuned per job by the decoration config's ``jobs`` tier. Python's ``AnyJob.execute`` returns its
``BatchResult`` with no separate error value, so with recovery on a failed run (a coded error,
an unexpected exception coded INTERNAL by the recovery layer) becomes one FAIL result instead of
an exception — the gate sees every job finish.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppError
from techai_webutils.core.interfaces.execution import (
    BatchResult,
    JobMeta,
    StepResult,
    StepStatus,
)
from techai_webutils.foundation.decorate import Deps, Layer, Tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import AnyJob
    from techai_webutils.foundation.decorate import Decorations, Subject


def tier() -> Tier:
    """Return the job tier, outermost first: recovery, logging, tracing, metrics, timeout."""
    return Tier(
        "job",
        (Layer.RECOVERY, Layer.LOGGING, Layer.TRACING, Layer.METRICS, Layer.TIMEOUT),
    )


class JobBase:
    """Builds decorated jobs: each runs as ``Execute`` through the tier under its name."""

    __slots__ = ("_deps",)

    def __init__(self, deps: Deps | None = None) -> None:
        """Bind the jobs' collaborators (none decorates nothing but recovery)."""
        self._deps = deps or Deps()

    @classmethod
    def from_decorations(cls, name: str, decorations: Decorations) -> JobBase:
        """Return the base whose jobs get the layers the config resolves for ``name``."""
        return cls(decorations.deps(tier(), name))

    def wrap(self, job: AnyJob) -> AnyJob:
        """Return ``job`` run through the tier."""
        return self.func(job.meta(), job.execute)

    def func(self, meta: JobMeta, run: Callable[[], Awaitable[BatchResult]]) -> AnyJob:
        """Return the job ``meta`` whose ``run`` runs through the tier."""
        return _DecoratedJob(
            meta,
            run,
            tier().subject(meta.name, self._deps),
            recover=not self._deps.skip_recovery,
        )


class _DecoratedJob:
    """A job run through the job tier (satisfies ``AnyJob``)."""

    __slots__ = ("_meta", "_op", "_recover", "_run")

    def __init__(
        self,
        meta: JobMeta,
        run: Callable[[], Awaitable[BatchResult]],
        op: Subject,
        *,
        recover: bool,
    ) -> None:
        """Bind the job's identity, its run, its subject and whether a failure becomes FAIL."""
        self._meta = meta
        self._run = run
        self._op = op
        self._recover = recover

    def meta(self) -> JobMeta:
        """Return the job's identity."""
        return self._meta

    async def execute(self) -> BatchResult:
        """Run the job through the tier; with recovery a failed run is one FAIL result."""
        try:
            return await self._op.exec("Execute", self._run)
        except AppError as exc:
            if not self._recover:
                raise
            failed = StepResult(
                name=self._meta.name,
                group=self._meta.group,
                status=StepStatus.FAIL,
                error=str(exc),
            )
            return BatchResult(results=(failed,))
