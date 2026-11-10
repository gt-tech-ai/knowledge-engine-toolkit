"""Unit tests for the degrading annotator decorator."""

from __future__ import annotations

from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.nlp import (
    AnnotatorConfig,
    AnnotatorKind,
    DegradingAnnotator,
    OnUnavailable,
    annotator_from_config,
)
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.annotator import (
    ENTITIES,
    AnnotateClient,
    Annotation,
    Annotator,
    TextBlock,
)
from techai_webutils.core.interfaces.metrics import MetricsProvider

_BLOCKS = [TextBlock("a", "c1"), TextBlock("b", "c2")]
_TASKS = frozenset({ENTITIES})


def _primary(error: Exception) -> Annotator:
    """Return a mocked annotator failing with ``error``.

    Returns:
        A mocked annotator failing with ``error``.

    """
    primary = MagicMock(spec=Annotator)
    primary.annotate_batch = AsyncMock(side_effect=error)
    return primary


@pytest.mark.asyncio
async def test_transient_failure_degrades_and_is_counted() -> None:
    """Test that an unavailable backend yields empty, degraded annotations.

    Counted per task.

    **Why this test is important:**
      - Enrichment must not stall ingestion while the NLP service is down, and the gap
        must be visible (an alert on ``nlp_degraded_total``), never silent.

    **What it tests:**
      - a primary raising ``UNAVAILABLE`` gives one ``Annotation(degraded=True)`` per
        block, with no entities
      - ``nlp_degraded_total`` is incremented by the block count for each requested task
    """
    metrics = MagicMock(spec=MetricsProvider)
    counter = cast("object", cast("MagicMock", metrics.counter).return_value)
    annotator = DegradingAnnotator(
        _primary(AppError(ErrorCode.UNAVAILABLE, "breaker open")), metrics=metrics
    )

    annotations = await annotator.annotate_batch(_BLOCKS, _TASKS, "")

    assert annotations == [Annotation(degraded=True), Annotation(degraded=True)]
    cast("MagicMock", cast("MagicMock", metrics.counter).assert_called_once_with)(
        "nlp_degraded_total",
        "Blocks left unannotated because the NLP backend was unavailable.",
        ["task"],
    )
    cast("MagicMock", counter.inc.assert_called_once_with)(2.0, task=ENTITIES)


@pytest.mark.asyncio
async def test_permanent_errors_propagate_and_wait_mode_reraises() -> None:
    """Test that a permanent error is never masked, and ``wait``.

    (the default) re-raises a transient one.

    **Why this test is important:**
      - Degrading a bad request would hide a bug as "the service was down"; and a
        deployment that did not opt in must keep retrying rather than store unannotated
        chunks.

    **What it tests:**
      - the decorator re-raises an ``INVALID_INPUT`` from the primary
      - a factory-built ``grpc`` annotator with the default ``on_unavailable`` re-raises
        ``UNAVAILABLE``; with ``degrade`` it returns degraded annotations
    """
    permanent = DegradingAnnotator(_primary(AppValueError("bad task")))
    client = MagicMock()
    client.annotate_batch = AsyncMock(side_effect=AppError(ErrorCode.UNAVAILABLE, "down"))
    waiting = annotator_from_config(
        AnnotatorConfig(kind=AnnotatorKind.GRPC), client=cast("AnnotateClient", client)
    )
    degrading = annotator_from_config(
        AnnotatorConfig(kind=AnnotatorKind.GRPC, on_unavailable=OnUnavailable.DEGRADE),
        client=cast("AnnotateClient", client),
    )

    with pytest.raises(AppValueError):
        _ = await permanent.annotate_batch(_BLOCKS, _TASKS, "")
    with pytest.raises(AppError) as waited:
        _ = await waiting.annotate_batch(_BLOCKS, _TASKS, "")
    degraded = await degrading.annotate_batch(_BLOCKS, _TASKS, "")

    assert waited.value.code is ErrorCode.UNAVAILABLE
    assert all(a.degraded for a in degraded)
    with pytest.raises(AppValueError):
        _ = annotator_from_config(
            AnnotatorConfig(on_unavailable=cast("OnUnavailable", cast("object", "bogus")))
        )
