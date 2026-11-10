"""``ThresholdEvaluator`` — the single-backend ``Evaluator``.

(no ``Kind`` until a second exists).

A case passes when every grader passed **and** the mean grader score reaches
``pass_threshold``; its score is that mean. A case with no graders fails with score
``0.0`` — an ungraded case must never count as a pass.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.interfaces.evaluator import EvalResult, Evaluator

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.dataset import EvalCase
    from techai_webutils.core.interfaces.grader import EvalOutput, Grader


@final
class ThresholdEvaluator(Evaluator):
    """Passes a case on the AND of its grades plus a mean-score threshold."""

    def __init__(self, pass_threshold: float) -> None:
        """Set the mean score a case must reach (``[0.0, 1.0]``).

        Args:
            pass_threshold: The mean score a case must reach.

        """
        self._pass_threshold = pass_threshold

    @override
    async def evaluate(
        self, case: EvalCase, output: EvalOutput, graders: Sequence[Grader]
    ) -> EvalResult:
        """Run every grader concurrently and aggregate their verdicts.

        Args:
            case: The eval case the output answers.
            output: What the system under test produced for ``case``.
            graders: The graders whose verdicts are aggregated.

        Returns:
            The aggregate evaluation.

        """
        grades = tuple(
            await asyncio.gather(*(grader.grade(case, output) for grader in graders))
        )
        if not grades:
            return EvalResult(case_id=case.id, grades=(), passed=False, score=0.0)
        score = sum(grade.score for grade in grades) / len(grades)
        passed = all(grade.passed for grade in grades) and score >= self._pass_threshold
        return EvalResult(case_id=case.id, grades=grades, passed=passed, score=score)
