"""Tests for pipelines.then (Go ``pipeline.Then``)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors.errors import ConflictError
from techai_webutils.pipelines import then
from techai_webutils.pipelines.base import BaseAsyncPipeline
from techai_webutils.pipelines.decorators import AsyncPipelineBuilder

_double = AsyncMock(side_effect=lambda x: x * 2)


_label = AsyncMock(side_effect=lambda x: f"n={x}")


@pytest.mark.asyncio
async def test_then_feeds_the_first_output_to_the_second() -> None:
    """then(first, second) is one pipeline whose stages run in order.

    **Why this test is important:**
      - Multi-stage pipelines are compositions, not one stage per service method

    **What it tests:**
      - The second stage receives the first's output; nesting gives three stages
    """
    piped = then(BaseAsyncPipeline(_double), BaseAsyncPipeline(_label))
    assert await piped.execute(3) == "n=6"
    three = then(
        BaseAsyncPipeline(_double),
        then(BaseAsyncPipeline(_double), BaseAsyncPipeline(_label)),
    )
    assert await three.execute(1) == "n=4"


@pytest.mark.asyncio
async def test_then_stops_at_the_first_failure() -> None:
    """A failing first stage short-circuits with its error unchanged.

    **Why this test is important:**
      - The error code must reach the transport intact and later stages must not run

    **What it tests:**
      - ConflictError propagates; the second stage is never awaited
    """

    fail = AsyncMock(side_effect=ConflictError("dup"))

    second = AsyncMock()
    with pytest.raises(ConflictError):
        await then(BaseAsyncPipeline(fail), second).execute(1)
    second.execute.assert_not_awaited()


@pytest.mark.asyncio
async def test_then_keeps_each_stage_decorated() -> None:
    """Each decorated stage keeps its own decorators when composed.

    **Why this test is important:**
      - One span/metric/log per stage is how a slow or failing stage is found by name

    **What it tests:**
      - Both stages log under their own names
    """
    logger = MagicMock()
    first = (
        AsyncPipelineBuilder(BaseAsyncPipeline(_double), "double")
        .with_logging(logger)
        .build()
    )
    second = (
        AsyncPipelineBuilder(BaseAsyncPipeline(_label), "label")
        .with_logging(logger)
        .build()
    )
    assert await then(first, second).execute(2) == "n=4"
    names = [c.kwargs.get("pipeline") for c in logger.debug.call_args_list]
    assert names == ["double", "label"]


def test_then_rejects_a_missing_stage() -> None:
    """A None stage is a wiring bug and fails at composition time.

    **Why this test is important:**
      - Failing at boot beats a None error on the first request

    **What it tests:**
      - ValueError
    """
    with pytest.raises(ValueError, match="must not be None"):
        then(BaseAsyncPipeline(_double), None)  # type: ignore[arg-type]
