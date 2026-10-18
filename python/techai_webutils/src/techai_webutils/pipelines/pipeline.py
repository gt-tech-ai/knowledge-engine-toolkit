"""The pipeline tier (mirrors Go ``pipelines/pipeline``).

``tier()`` orders the tier's layers; ``PipelineBase``, built from the pipeline's contract, runs
each operation through it, and ``PipelineBase.stage`` builds one as a decorated ``Stage`` — one
span, metric series and log line per stage — that ``Stage.then`` chains into a pipeline.
``PipelineBase.fan_out`` runs stages concurrently over one input.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.pipeline import AsyncPipeline
from techai_webutils.foundation.decorate import Layer, Tier

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
    """Runs one pipeline's operations through the pipeline tier (Go ``pipeline.Base``).

    Each operation is one method of the pipeline's contract, decorated as ``<name>.<method>``;
    ``stage`` builds an operation as a composable Stage.
    """

    __slots__ = ("_subject",)

    def __init__(self, contract: type, decorations: Decorations, name: str) -> None:
        """Bind the pipeline ``name``: one chain per method of ``contract`` (Go ``NewBase[P]``)."""
        self._subject = decorations.subject_of(tier(), name, contract)

    @property
    def name(self) -> str:
        """The pipeline's name."""
        return self._subject.name

    async def exec[R](self, op: str, fn: Callable[[], Awaitable[R]]) -> R:
        """Run ``fn`` as the operation ``op`` through the tier."""
        return await self._subject.exec(op, fn)

    def stage[In, Out](
        self, op: str, fn: Callable[[In], Awaitable[Out]]
    ) -> Stage[In, Out]:
        """Return ``fn`` as the decorated operation ``op``, a stage that composes with ``then``.

        A None function is a wiring bug and raises ``AppValueError``.
        """
        if fn is None:  # pyright: ignore[reportUnnecessaryComparison]
            msg = "pipeline: stage fn must not be None"
            raise AppValueError(msg)
        subject = self._subject

        async def run(input_data: In) -> Out:
            return await subject.exec(op, lambda: fn(input_data))

        return Stage(run)

    def fan_out[In, Out](
        self, op: str, *stages: AsyncPipeline[In, Out]
    ) -> Stage[In, list[Out]]:
        """Return the decorated operation ``op``, a stage that runs ``stages`` concurrently.

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

        return self.stage(op, run)
