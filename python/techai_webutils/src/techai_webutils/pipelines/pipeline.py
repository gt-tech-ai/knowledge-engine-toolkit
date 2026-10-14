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

    from techai_webutils.foundation.decorate import Decorations


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

    __slots__ = ("_deps",)

    def __init__(self, deps: Deps | None = None) -> None:
        """Bind the stages' collaborators (none decorates nothing but recovery)."""
        self._deps = deps or Deps()

    @classmethod
    def from_decorations(cls, name: str, decorations: Decorations) -> PipelineBase:
        """Return the base whose stages get the layers the config resolves for ``name``."""
        return cls(decorations.deps(tier(), name))

    def stage[In, Out](
        self, name: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Stage[In, Out]:
        """Return ``fn`` as the stage ``name``, run through the tier.

        A None function is a wiring bug and raises ``AppValueError``.
        """
        if fn is None:  # pyright: ignore[reportUnnecessaryComparison]
            msg = "pipeline: stage fn must not be None"
            raise AppValueError(msg)
        subject = tier().subject(name, self._deps)

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
