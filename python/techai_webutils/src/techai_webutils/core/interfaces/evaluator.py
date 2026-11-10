"""``Evaluator`` — aggregates grader verdicts into one result per case.

An eval run feeds each ``EvalCase`` through the system under test, captures an
``EvalOutput`` and asks the evaluator to grade it with a set of ``Grader``s; the run folds
the ``EvalResult``s into an ``EvalReport`` keyed on the dataset version. ``EvalOutput``
lives with the ``Grader`` contract, which consumes it.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.dataset import EvalCase
    from techai_webutils.core.interfaces.grader import EvalOutput, Grader, GradeResult


@dataclass(frozen=True, slots=True)
class EvalResult:
    """One case's graded result."""

    case_id: str
    """The ``EvalCase.id`` this result is for."""
    grades: tuple[GradeResult, ...]
    """Each grader's verdict, in grader order."""
    passed: bool
    """Whether the case passed overall (the evaluator's aggregation)."""
    score: float
    """The aggregate score in ``[0.0, 1.0]``."""


@dataclass(frozen=True, slots=True)
class EvalReport:
    """A whole run's summary against one dataset version."""

    dataset_version: str
    """The ``Dataset.version()`` the run measured."""
    pass_rate: float
    """The fraction of cases that passed."""
    mean_score: float
    """The mean aggregate score across cases."""
    per_grader: Mapping[str, float]
    """The mean score per grader name."""
    regressions: tuple[str, ...]
    """Ids of cases that passed in the baseline and fail now."""


class Evaluator(ABC):
    """Aggregates grader verdicts for one case."""

    @abstractmethod
    async def evaluate(
        self, case: EvalCase, output: EvalOutput, graders: Sequence[Grader]
    ) -> EvalResult:
        """Grade ``output`` for ``case`` with every grader and aggregate the verdicts.

        Args:
            case: The eval case the output answers.
            output: What the system under test produced for ``case``.
            graders: The graders whose verdicts are aggregated.

        Returns:
            Every grader's verdict and the case's aggregate pass and score.

        """
