"""Unit tests for the entity ruler wrapper."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.nlp import EntityRulerAnnotator, RulerPattern
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.annotator import (
    ENTITIES,
    Annotation,
    Annotator,
    Entity,
    TextBlock,
)

_TEXT = "Project Blue Heron ships with heron-db; Ann leads it."
_STATISTICAL = (
    Entity("ORG", 8, 12, "Blue"),
    Entity("PERSON", 40, 43, "Ann"),
)


def _inner() -> MagicMock:
    """Return a mocked statistical annotator that finds ``_STATISTICAL``.

    In every block.

    Returns:
        A mocked statistical annotator that finds ``_STATISTICAL``.

    """
    inner = MagicMock(spec=Annotator)
    inner.annotate_batch = AsyncMock(return_value=[Annotation(entities=_STATISTICAL)])
    return inner


@pytest.mark.asyncio
async def test_pattern_spans_win_over_overlapping_statistical_spans() -> None:
    """Test that an org's phrases are found whole.

    And replace the model's overlapping guesses.

    **Why this test is important:**
      - A statistical model splits or mislabels an org's own names; the ruler is how an
        org makes ``Blue Heron`` one ``PRODUCT``, not an ``ORG`` called ``Blue``.

    **What it tests:**
      - the longest phrase wins (``Blue Heron`` over ``Blue``), matched case-insensitively
        and only as whole words (``heron-db`` is not ``Heron``)
      - the overlapping statistical ``ORG`` is dropped; the separate ``PERSON`` is kept,
        and the result is in text order
    """
    patterns = [
        RulerPattern("PRODUCT", "blue heron"),
        RulerPattern("COLOR", "Blue"),
        RulerPattern("PRODUCT", "heron"),
    ]
    annotator = EntityRulerAnnotator(_inner(), patterns)

    [annotation] = await annotator.annotate_batch(
        [TextBlock(_TEXT)], frozenset({ENTITIES}), ""
    )

    assert annotation.entities == (
        Entity("PRODUCT", 8, 18, "Blue Heron"),
        Entity("PRODUCT", 30, 35, "heron"),
        Entity("PERSON", 40, 43, "Ann"),
    )


@pytest.mark.asyncio
async def test_no_patterns_or_no_entities_task_leaves_output_unchanged() -> None:
    """Test that the ruler is a pass-through when it has nothing to do.

    **Why this test is important:**
      - Wrapping every org's annotator must cost nothing for an org with no patterns, and
        must not add entities to a call that did not ask for them.

    **What it tests:**
      - with no patterns, or without the ``entities`` task, the inner annotations come
        back as they were
      - a blank label or phrase is refused with ``AppValueError``
    """
    block = [TextBlock(_TEXT)]

    empty = await EntityRulerAnnotator(_inner(), []).annotate_batch(
        block, frozenset({ENTITIES}), ""
    )
    other = await EntityRulerAnnotator(
        _inner(), [RulerPattern("PRODUCT", "Blue Heron")]
    ).annotate_batch(block, frozenset({"pii"}), "")

    assert empty == [Annotation(entities=_STATISTICAL)]
    assert other == [Annotation(entities=_STATISTICAL)]
    for label, phrase in (("", "x"), ("X", " ")):
        with pytest.raises(AppValueError):
            RulerPattern(label, phrase)
