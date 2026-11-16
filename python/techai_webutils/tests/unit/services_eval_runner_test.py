"""Unit tests for ``run_eval``: a dataset run through a system, graded into a report."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.dataset import Dataset, EvalCase
from techai_webutils.core.interfaces.evaluator import EvalReport, EvalResult
from techai_webutils.core.interfaces.grader import EvalOutput, Grader, GradeResult
from techai_webutils.services.eval_runner import run_eval
from techai_webutils.services.evaluator import ThresholdEvaluator

_VERSION = "v-hash"
"""The mocked dataset's content hash."""
_THRESHOLD = 0.5
"""The score a grade and a case must reach to pass."""


def _dataset(*case_ids: str) -> Dataset:
    """Return a mocked dataset yielding one case per id, in order.

    Args:
        *case_ids: The case ids.

    Returns:
        The mocked dataset at version ``_VERSION``.

    """
    cases = [
        EvalCase(id=case_id, inputs={"query": case_id}, expected={}, metadata={})
        for case_id in case_ids
    ]
    stream = MagicMock()
    stream.configure_mock(**{"__aiter__.return_value": cases})
    dataset = MagicMock(spec=Dataset)
    dataset.cases = MagicMock(return_value=stream)
    dataset.version = MagicMock(return_value=_VERSION)
    return dataset


def _answer(case: EvalCase) -> EvalOutput:
    """Answer ``ok`` for case ``c1`` and ``wrong`` for every other case.

    Args:
        case: The case.

    Returns:
        The system's output.

    """
    text = "ok" if case.id == "c1" else "wrong"
    return EvalOutput(answer=text, citations=(), trace_id=f"t-{case.id}")


def _grader(name: str, good: float, bad: float) -> Grader:
    """Return a mocked grader scoring an ``ok`` answer ``good`` and any other ``bad``.

    Args:
        name: The grader's name.
        good: The score for ``ok``.
        bad: The score for anything else.

    Returns:
        The mocked grader; a grade passes at ``_THRESHOLD`` or above.

    """

    def grade(case: EvalCase, output: EvalOutput) -> GradeResult:
        """Score the output.

        Args:
            case: The case.
            output: The system's output.

        Returns:
            The verdict.

        """
        score = good if output.answer == "ok" else bad
        return GradeResult(
            grader=name, score=score, passed=score >= _THRESHOLD, rationale=case.id
        )

    grader = MagicMock(spec=Grader)
    grader.grade = AsyncMock(side_effect=grade)
    return grader


def _baseline(*passed: str, version: str = _VERSION) -> EvalReport:
    """Build a baseline report in which the ``passed`` cases passed.

    Args:
        *passed: The ids that passed in the baseline.
        version: The dataset version the baseline was measured on.

    Returns:
        The baseline.

    """
    return EvalReport(
        dataset_version=version,
        pass_rate=1.0,
        mean_score=1.0,
        per_grader={},
        regressions=(),
        results=tuple(
            EvalResult(case_id=case_id, grades=(), passed=True, score=1.0)
            for case_id in passed
        ),
    )


@pytest.mark.asyncio
async def test_run_eval_reports_every_case_and_the_means() -> None:
    """Test that a run grades every case and summarizes them in one report.

    **Why this test is important:**
      - The report gates promotion of a model or prompt; per-case results are what a
        reviewer reads to see which cases moved, and the means are what the gate reads.

    **What it tests:**
      - two cases, ``c1`` answered well and ``c2`` badly, graded by ``a`` (1.0 / 0.0)
        and ``b`` (0.8 / 0.4) → ``results`` in dataset order with ``c1`` passed and
        ``c2`` failed, ``pass_rate == 0.5``, ``mean_score == 0.55``, ``per_grader ==
        {a: 0.5, b: 0.6}``, the dataset's version, and no regressions without a baseline
      - the system is called once per case
    """
    system = AsyncMock(side_effect=_answer)

    report = await run_eval(
        _dataset("c1", "c2"),
        system,
        [_grader("a", 1.0, 0.0), _grader("b", 0.8, 0.4)],
        ThresholdEvaluator(pass_threshold=_THRESHOLD),
    )

    assert [(r.case_id, r.passed) for r in report.results] == [
        ("c1", True),
        ("c2", False),
    ]
    assert report.dataset_version == _VERSION
    assert report.pass_rate == pytest.approx(0.5)
    assert report.mean_score == pytest.approx(0.55)
    assert dict(report.per_grader) == pytest.approx({"a": 0.5, "b": 0.6})
    assert report.regressions == ()
    assert system.await_count == len(report.results)


@pytest.mark.asyncio
async def test_run_eval_lists_cases_that_regressed_from_the_baseline() -> None:
    """Test that a case passed in the baseline and failing now is a regression.

    **Why this test is important:**
      - A regression is the signal a change broke something that worked; a case that
        failed before and still fails is not one.

    **What it tests:**
      - a baseline in which ``c1`` and ``c2`` passed; now ``c2`` fails →
        ``regressions == ("c2",)``
    """
    report = await run_eval(
        _dataset("c1", "c2", "c3"),
        AsyncMock(side_effect=_answer),
        [_grader("a", 1.0, 0.0)],
        ThresholdEvaluator(pass_threshold=_THRESHOLD),
        baseline=_baseline("c1", "c2"),
    )

    assert report.regressions == ("c2",)


@pytest.mark.asyncio
async def test_run_eval_rejects_a_baseline_from_another_dataset_version() -> None:
    """Test that a baseline measured on other cases is refused.

    **Why this test is important:**
      - A dataset's version is its content hash; comparing against a baseline from a
        changed dataset would report regressions that are only changed cases.

    **What it tests:**
      - a baseline at version ``old`` → ``AppError`` with ``INVALID_INPUT``, before the
        system is called
    """
    system = AsyncMock(side_effect=_answer)

    with pytest.raises(AppError) as raised:
        _ = await run_eval(
            _dataset("c1"),
            system,
            [_grader("a", 1.0, 0.0)],
            ThresholdEvaluator(pass_threshold=_THRESHOLD),
            baseline=_baseline("c1", version="old"),
        )

    assert raised.value.code == ErrorCode.INVALID_INPUT
    system.assert_not_awaited()


@pytest.mark.asyncio
async def test_an_empty_dataset_reports_zero_rates() -> None:
    """Test that a run over no cases reports zeros rather than dividing by zero.

    **Why this test is important:**
      - An empty dataset is a misconfiguration the gate must see as a failure, not a
        crash and not a perfect score.

    **What it tests:**
      - no cases → ``results == ()``, ``pass_rate == 0.0``, ``mean_score == 0.0``,
        ``per_grader == {}``
    """
    report = await run_eval(
        _dataset(),
        AsyncMock(side_effect=_answer),
        [_grader("a", 1.0, 0.0)],
        ThresholdEvaluator(pass_threshold=_THRESHOLD),
    )

    assert (report.results, report.pass_rate, report.mean_score) == ((), 0.0, 0.0)
    assert dict(report.per_grader) == {}
