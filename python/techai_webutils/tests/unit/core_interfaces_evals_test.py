"""Unit tests for the eval contracts: ``EvalCase``.

``GradeResult`` and the eval value types.
"""

from __future__ import annotations

import dataclasses

import pytest

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.dataset import EvalCase
from techai_webutils.core.interfaces.evaluator import EvalOutput, EvalReport, EvalResult
from techai_webutils.core.interfaces.grader import GradeResult


def test_eval_case_rejects_empty_id_with_invalid_input() -> None:
    """Test that an eval case cannot be built without an id.

    **Why this test is important:**
      - The case id keys every result and every regression in a report; an id-less case
        would make a regression unattributable.

    **What it tests:**
      - ``EvalCase(id="")`` raises ``AppError(INVALID_INPUT)``
      - a case with an id keeps its inputs, expected values and metadata as given
    """
    with pytest.raises(AppError) as caught:
        EvalCase(id="", inputs={"query": "q"}, expected={}, metadata={})

    assert caught.value.code is ErrorCode.INVALID_INPUT
    case = EvalCase(
        id="c1", inputs={"query": "q"}, expected={"answer": "a"}, metadata={"tag": "t"}
    )
    assert (case.id, case.inputs["query"], case.expected["answer"]) == ("c1", "q", "a")
    assert case.metadata == {"tag": "t"}


def test_eval_value_types_are_frozen() -> None:
    """Test that the eval value types are immutable.

    **Why this test is important:**
      - A case, an output and a grade flow through the dataset, the graders and the
        report; a mutation between them would grade one answer and report another.

    **What it tests:**
      - assigning a field on ``EvalCase``, ``EvalOutput``, ``GradeResult``, ``EvalResult``
        or ``EvalReport`` raises ``FrozenInstanceError``
    """
    grade = GradeResult(grader="g", score=1.0, passed=True, rationale="ok")
    values: list[object] = [
        EvalCase(id="c1", inputs={}, expected={}, metadata={}),
        EvalOutput(answer="a", citations=("doc-1",), trace_id="t"),
        grade,
        EvalResult(case_id="c1", grades=(grade,), passed=True, score=1.0),
        EvalReport(
            dataset_version="v",
            pass_rate=1.0,
            mean_score=1.0,
            per_grader={"g": 1.0},
            regressions=(),
        ),
    ]

    for value in values:
        field = dataclasses.fields(value)[0].name  # pyright: ignore[reportArgumentType]
        with pytest.raises(dataclasses.FrozenInstanceError):
            setattr(value, field, None)
