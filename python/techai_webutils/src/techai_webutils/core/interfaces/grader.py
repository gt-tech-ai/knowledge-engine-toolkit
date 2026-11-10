"""``Grader`` — scores one system output against one eval case.

Graders are the unit an ``Evaluator`` aggregates; ``EvalOutput`` is what they grade. Only
the contract and a ``stub`` grader live in the toolkit today; code and LLM-judge graders
arrive with their own ``Kind`` tier.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.dataset import EvalCase


@dataclass(frozen=True, slots=True)
class EvalOutput:
    """What the system under test produced for one case."""

    answer: str
    """The generated answer text."""
    citations: tuple[str, ...]
    """Ids of the sources the answer cites, in order."""
    trace_id: str
    """The OTel trace id of the run (links a result to its trace)."""
    retrieved: tuple[str, ...] = ()
    """Ids of the documents retrieval ranked for the case, best first (ranking
    graders)."""


@dataclass(frozen=True, slots=True)
class GradeResult:
    """One grader's verdict on one output."""

    grader: str
    """The grader's name (keys ``EvalReport.per_grader``)."""
    score: float
    """The score in ``[0.0, 1.0]``."""
    passed: bool
    """Whether the output met this grader's bar."""
    rationale: str
    """Why the grader scored it so (human-readable)."""


class Grader(ABC):
    """Scores a system output for an eval case."""

    @abstractmethod
    async def grade(self, case: EvalCase, output: EvalOutput) -> GradeResult:
        """Return this grader's verdict on ``output`` for ``case``."""
