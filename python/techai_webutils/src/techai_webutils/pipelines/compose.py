"""Pipeline composition (mirrors Go ``pipeline.Then``)."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.pipelines.base import BaseAsyncPipeline

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.pipeline import AsyncPipeline


def then[A, B, C](
    first: AsyncPipeline[A, B], second: AsyncPipeline[B, C]
) -> AsyncPipeline[A, C]:
    """Compose two pipelines into one: ``first``'s output is ``second``'s input.

    An error from ``first`` propagates unchanged and ``second`` does not run. Each stage keeps
    its own decorators, so building each with ``AsyncPipelineBuilder`` before composing gives
    one span, metric series and log line per stage. ``then`` nests:
    ``then(a, then(b, c))`` is a three-stage pipeline. A ``None`` stage is a wiring bug and
    raises ``ValueError`` at composition time.
    """
    if first is None or second is None:  # pyright: ignore[reportUnnecessaryComparison]
        msg = "pipeline: then stages must not be None"
        raise ValueError(msg)

    async def run(input_data: A) -> C:
        return await second.execute(await first.execute(input_data))

    return BaseAsyncPipeline(run)
