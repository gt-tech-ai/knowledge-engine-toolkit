"""The ``stub`` grader: a deterministic always-pass verdict (zero infrastructure)."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.grader import Grader, GradeResult

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.dataset import EvalCase
    from techai_webutils.core.interfaces.grader import EvalOutput


class StubGrader(Grader):
    """Passes every case with a perfect score.

    An eval run then works before real graders exist.
    """

    @override
    async def grade(self, case: EvalCase, output: EvalOutput) -> GradeResult:
        """Return a perfect, passing verdict.

        Args:
            case: The eval case the output answers.
            output: What the system under test produced for ``case``.

        Returns:
            A perfect, passing verdict.

        """
        return GradeResult(grader="stub", score=1.0, passed=True, rationale="stub grader")
