"""``run_eval``: run a dataset through a system under test and grade it into a report.

Each ``EvalCase`` is answered by ``system``, graded by the ``Evaluator`` with every
``Grader``, and the per-case ``EvalResult``s are folded into one ``EvalReport``: the pass
rate, the mean score, each grader's mean score, and, against a baseline report from the
same dataset version, the cases that regressed. Cases run one at a time in dataset order,
so a run's cost and the system's load stay predictable; graders for one case run
concurrently inside the evaluator.
"""

from __future__ import annotations

from collections import defaultdict
from statistics import fmean
from typing import TYPE_CHECKING

from techai_webutils.core.errors import InvalidInputError
from techai_webutils.core.interfaces.evaluator import EvalReport

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Sequence

    from techai_webutils.core.interfaces.dataset import Dataset, EvalCase
    from techai_webutils.core.interfaces.evaluator import EvalResult, Evaluator
    from techai_webutils.core.interfaces.grader import EvalOutput, Grader


async def run_eval(
    dataset: Dataset,
    system: Callable[[EvalCase], Awaitable[EvalOutput]],
    graders: Sequence[Grader],
    evaluator: Evaluator,
    *,
    baseline: EvalReport | None = None,
) -> EvalReport:
    """Answer and grade every case in ``dataset`` and summarize the run.

    Args:
        dataset: The cases, and the version the report is keyed on.
        system: The system under test: answers one case.
        graders: The graders every case is graded by.
        evaluator: Aggregates one case's grades into its result.
        baseline: An earlier report on the same dataset version; the cases that passed
            in it and fail now are the regressions.

    Returns:
        The report; an empty dataset gives zero rates and no results.

    Raises:
        InvalidInputError: When ``baseline`` was measured on another dataset version;
            raised before any case is run.

    """
    version = dataset.version()
    if baseline is not None and baseline.dataset_version != version:
        msg = (
            f"baseline dataset version {baseline.dataset_version!r} does not match "
            f"{version!r}"
        )
        raise InvalidInputError(msg)
    results = [
        await evaluator.evaluate(case, await system(case), graders)
        async for case in dataset.cases()
    ]
    return EvalReport(
        dataset_version=version,
        pass_rate=_mean([1.0 if result.passed else 0.0 for result in results]),
        mean_score=_mean([result.score for result in results]),
        per_grader=_per_grader(results),
        regressions=_regressions(results, baseline),
        results=tuple(results),
    )


def _mean(values: Sequence[float]) -> float:
    """Average ``values``, or ``0.0`` when there are none.

    Args:
        values: The values.

    Returns:
        Their mean.

    """
    return fmean(values) if values else 0.0


def _per_grader(results: Sequence[EvalResult]) -> dict[str, float]:
    """Average each grader's score across every case it graded.

    Args:
        results: The run's results.

    Returns:
        The mean score by grader name.

    """
    scores: defaultdict[str, list[float]] = defaultdict(list)
    for result in results:
        for grade in result.grades:
            scores[grade.grader].append(grade.score)
    return {name: fmean(values) for name, values in scores.items()}


def _regressions(
    results: Sequence[EvalResult], baseline: EvalReport | None
) -> tuple[str, ...]:
    """List the cases that passed in ``baseline`` and fail now.

    Args:
        results: The run's results.
        baseline: The earlier report, or ``None``.

    Returns:
        The regressed case ids, in dataset order; none without a baseline.

    """
    if baseline is None:
        return ()
    passed_before = {result.case_id for result in baseline.results if result.passed}
    return tuple(
        result.case_id
        for result in results
        if not result.passed and result.case_id in passed_before
    )
