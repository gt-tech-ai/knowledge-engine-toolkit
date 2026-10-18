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

    __slots__ = ("_decorations", "_deps", "_prefix")

    def __init__(self, deps: Deps | None = None) -> None:
        """Bind the collaborators every job shares (none decorates nothing but recovery)."""
        self._deps = deps or Deps()
        self._decorations: Decorations | None = None
        self._prefix = ""

    @classmethod
    def from_decorations(cls, name: str, decorations: Decorations) -> JobBase:
        """Return the base whose jobs each take their own layers from ``decorations``.

        A job named ``job`` is the job-tier component ``<name>.<job>`` (Go ``NewBaseFrom``), so
        the config can tune one job, or every job under ``name``.
        """
        base = cls()
        base._decorations = decorations
        base._prefix = name
        return base

    def named(self, sub: str) -> JobBase:
        """Return the base whose components sit beneath ``sub`` (Go ``Base.Named``).

        From a base of prefix ``events``, ``named("sweep")`` makes a job ``reap`` the component ``events.sweep.reap``. A base built from
        shared Deps is returned unchanged.
        """
        if self._decorations is None:
            return self
        return type(self).from_decorations(
            f"{self._prefix}.{sub}" if self._prefix else sub, self._decorations
        )

    def wrap(self, job: AnyJob) -> AnyJob:
        """Return ``job`` run through the tier."""
        return self.func(job.meta(), job.execute)

    def func(self, meta: JobMeta, run: Callable[[], Awaitable[BatchResult]]) -> AnyJob:
        """Return the job ``meta`` whose ``run`` runs through the tier."""
        name, deps = meta.name, self._deps
        if self._decorations is not None:
            name = f"{self._prefix}.{meta.name}" if self._prefix else meta.name
            deps = self._decorations.resolve(tier(), name)
        return _DecoratedJob(
            meta, run, tier().subject(name, deps), recover=not deps.skip_recovery
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
