"""Unit tests for ``ThresholdEvaluator`` and the ``stub`` grader."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.grader.stub import StubGrader
from techai_webutils.core.interfaces.dataset import EvalCase
from techai_webutils.core.interfaces.evaluator import EvalOutput
from techai_webutils.core.interfaces.grader import Grader, GradeResult
from techai_webutils.services.evaluator import ThresholdEvaluator

_CASE = EvalCase(id="c1", inputs={"query": "q"}, expected={"answer": "a"}, metadata={})
_OUTPUT = EvalOutput(answer="a", citations=("doc-1",), trace_id="trace-1")


def _grader(name: str, score: float, *, passed: bool) -> Grader:
    """Return a mocked grader that answers one fixed verdict."""
    grader = MagicMock(spec=Grader)
    grader.grade = AsyncMock(
        return_value=GradeResult(grader=name, score=score, passed=passed, rationale="r")
    )
    return grader


@pytest.mark.asyncio
async def test_threshold_evaluator_passes_on_and_of_grades() -> None:
    """Test that a case passes only when every grade passes.

    And the mean clears the threshold.

    **Why this test is important:**
      - The evaluator's verdict gates promotion; a single failing grader must fail the
        case even when the mean score is high, and a low mean must fail it even when every
        grader passed.

    **What it tests:**
      - grades 0.9 and 0.7 (both passed) at threshold 0.8 → ``score == 0.8``, ``passed is
        True``
      - the same scores with one grade failed → ``passed is False``
      - both passed but the mean (0.7) below 0.8 → ``passed is False``
      - no graders → ``passed is False``, ``score == 0.0``
      - every grade is kept on the result in grader order
    """
    evaluator = ThresholdEvaluator(pass_threshold=0.8)

    passing = await evaluator.evaluate(
        _CASE, _OUTPUT, [_grader("a", 0.9, passed=True), _grader("b", 0.7, passed=True)]
    )
    one_failed = await evaluator.evaluate(
        _CASE, _OUTPUT, [_grader("a", 0.9, passed=True), _grader("b", 0.7, passed=False)]
    )
    low_mean = await evaluator.evaluate(
        _CASE, _OUTPUT, [_grader("a", 0.7, passed=True), _grader("b", 0.7, passed=True)]
    )
    empty = await evaluator.evaluate(_CASE, _OUTPUT, [])

    assert passing.case_id == "c1"
    assert passing.score == pytest.approx(0.8)
    assert passing.passed is True
    assert [grade.grader for grade in passing.grades] == ["a", "b"]
    assert one_failed.passed is False
    assert low_mean.score == pytest.approx(0.7)
    assert low_mean.passed is False
    assert (empty.passed, empty.score, empty.grades) == (False, 0.0, ())


@pytest.mark.asyncio
async def test_stub_grader_always_passes() -> None:
    """Test that the stub grader passes every case with a perfect score.

    **Why this test is important:**
      - The stub grader lets an eval run work end to end before real graders exist; it
        must be deterministic so a stub run never flakes.

    **What it tests:**
      - ``grade`` returns ``GradeResult("stub", 1.0, True, "stub grader")``
    """
    result = await StubGrader().grade(_CASE, _OUTPUT)

    assert result == GradeResult(
        grader="stub", score=1.0, passed=True, rationale="stub grader"
    )
