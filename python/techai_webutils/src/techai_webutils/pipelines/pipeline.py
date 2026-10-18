"""The pipeline tier (mirrors Go ``pipelines/pipeline``).

``tier()`` orders the tier's layers; ``PipelineBase.stage`` turns a function into a decorated
``Stage`` — one span, metric series and log line per stage — and ``Stage.then`` chains stages
into a pipeline. ``PipelineBase.fan_out`` runs stages concurrently over one input.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.pipeline import AsyncPipeline
from techai_webutils.foundation.decorate import Deps, Layer, Tier

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.foundation.decorate import Decorations, Subject


def tier() -> Tier:
    """Return the pipeline tier, outermost first: recovery, logging, tracing, metrics, timeout."""
    return Tier(
        "pipeline",
        (Layer.RECOVERY, Layer.LOGGING, Layer.TRACING, Layer.METRICS, Layer.TIMEOUT),
    )


class Stage[In, Out](AsyncPipeline[In, Out]):
    """A pipeline built from a function; ``then`` chains it with the next stage."""

    __slots__ = ("_fn",)

    def __init__(self, fn: Callable[[In], Awaitable[Out]]) -> None:
        """Bind the stage's function."""
        self._fn = fn

    @override
    async def execute(self, input_data: In) -> Out:
        """Run the stage."""
        return await self._fn(input_data)

    def then[C](self, next_: AsyncPipeline[Out, C]) -> Stage[In, C]:
        """Return the pipeline that feeds this stage's output to ``next_``.

        An error from this stage propagates unchanged and ``next_`` does not run; each stage keeps
        its own decorators. A None stage is a wiring bug and raises ``AppValueError``.
        """
        if next_ is None:  # pyright: ignore[reportUnnecessaryComparison]
            msg = "pipeline: then stage must not be None"
            raise AppValueError(msg)

        async def run(input_data: In) -> C:
            return await next_.execute(await self._fn(input_data))

        return Stage(run)


class PipelineBase:
    """Builds decorated stages: each runs as ``Execute`` through the tier under its name."""

    __slots__ = ("_decorations", "_deps", "_prefix")

    def __init__(self, deps: Deps | None = None) -> None:
        """Bind the collaborators every stage shares (none decorates nothing but recovery)."""
        self._deps = deps or Deps()
        self._decorations: Decorations | None = None
        self._prefix = ""

    @classmethod
    def from_decorations(cls, name: str, decorations: Decorations) -> PipelineBase:
        """Return the base whose stages each take their own layers from ``decorations``.

        A stage named ``stage`` is the pipeline-tier component ``<name>.<stage>`` (Go
        ``NewBaseFrom``), so the config can tune one stage, or every stage under ``name``.
        """
        base = cls()
        base._decorations = decorations
        base._prefix = name
        return base

    def named(self, sub: str) -> PipelineBase:
        """Return the base whose components sit beneath ``sub`` (Go ``Base.Named``).

        From a base of prefix ``api``, ``named("documents")`` makes a stage ``create`` the component ``api.documents.create``. A base built from
        shared Deps is returned unchanged.
        """
        if self._decorations is None:
            return self
        return type(self).from_decorations(
            f"{self._prefix}.{sub}" if self._prefix else sub, self._decorations
        )

    def subject(self, name: str) -> Subject:
        """Return the decorated Subject of the pipeline-tier component ``name`` (Go ``Base.Subject``).

        For a pipeline not built from stage functions to run its operations through the tier as
        a stage would.
        """
        if self._decorations is None:
            return tier().subject(name, self._deps)
        key = f"{self._prefix}.{name}" if self._prefix else name
        return self._decorations.subject(tier(), key)

    def stage[In, Out](
        self, name: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Stage[In, Out]:
        """Return ``fn`` as the stage ``name``, run through the tier.

        A None function is a wiring bug and raises ``AppValueError``.
        """
        if fn is None:  # pyright: ignore[reportUnnecessaryComparison]
            msg = "pipeline: stage fn must not be None"
            raise AppValueError(msg)
        subject = self.subject(name)

        async def run(input_data: In) -> Out:
            return await subject.exec("Execute", lambda: fn(input_data))

        return Stage(run)

    def fan_out[In, Out](
        self, name: str, *stages: AsyncPipeline[In, Out]
    ) -> Stage[In, list[Out]]:
        """Return the stage ``name`` that runs ``stages`` concurrently over one input.

        The outputs keep the stages' order; the first failure cancels the rest and is raised.
        """

        async def run(input_data: In) -> list[Out]:
            try:
                async with asyncio.TaskGroup() as group:
                    tasks = [group.create_task(s.execute(input_data)) for s in stages]
            except ExceptionGroup as group_error:
                # The TaskGroup groups the failures; the caller gets the first, its code intact.
                raise group_error.exceptions[0] from None
            return [t.result() for t in tasks]

        return self.stage(name, run)
