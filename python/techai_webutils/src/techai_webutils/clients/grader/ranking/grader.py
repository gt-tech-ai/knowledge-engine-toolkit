"""The ``ranking`` grader.

Scores a retrieval ranking against a case's relevance judgments.

An eval case carries its relevant document ids in ``expected["relevant"]`` (a JSON list of
strings; binary relevance), and the output carries the ids retrieval ranked in
``retrieved``, best first. A repeated id (several passages of one document) counts at its
first rank only. Each grader scores one ``RankingMetric`` at a cutoff ``k``:

- ``recall``: the share of relevant documents in the top ``k``;
- ``ndcg``: the top ``k``'s discounted cumulative gain (``1 / log2(rank + 1)`` per
  relevant document) over the ideal ranking's;
- ``rr``: one over the rank of the first relevant document in the top ``k``, else 0 (its
  mean over a run is MRR@k).

A case with no relevant document (an unanswerable query) scores 1 when nothing is
retrieved, an abstention, and 0 otherwise, on every metric.
"""

from __future__ import annotations

import json
import math
from enum import StrEnum
from typing import TYPE_CHECKING, cast, final, override

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.grader import Grader, GradeResult

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.dataset import EvalCase
    from techai_webutils.core.interfaces.grader import EvalOutput

EXPECTED_RELEVANT_KEY = "relevant"
"""The eval case's ``expected`` key holding its relevant ids, as a JSON list."""


class RankingMetric(StrEnum):
    """Which ranking measure a ``RankingGrader`` scores."""

    RECALL = "recall"
    """The share of relevant documents in the top k."""
    NDCG = "ndcg"
    """Normalized discounted cumulative gain at k (binary relevance)."""
    RR = "rr"
    """Reciprocal rank of the first relevant document in the top k."""


def _relevant(case: EvalCase) -> frozenset[str]:
    """Return the case's relevant ids.

    Args:
        case: The eval case.

    Returns:
        The case's relevant ids.

    Raises:
        AppError: ``INVALID_INPUT`` when the judgments are missing or not a JSON list of
            strings (the eval set is wrong, not the ranking).

    """
    try:
        value = cast("object", json.loads(case.expected[EXPECTED_RELEVANT_KEY]))
    except (KeyError, ValueError) as exc:
        msg = f"eval case {case.id!r} has no readable relevant list"
        raise AppError(ErrorCode.INVALID_INPUT, msg) from exc
    items = cast("list[object]", value) if isinstance(value, list) else None
    ids = [v for v in items or [] if isinstance(v, str)]
    if items is None or len(ids) != len(items):
        msg = f"eval case {case.id!r}: relevant must be a JSON list of ids"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
    return frozenset(ids)


def _ranked(retrieved: Sequence[str]) -> list[str]:
    """Return ``retrieved`` with each id kept at its first rank only.

    Args:
        retrieved: The retrieved ids, best first, possibly repeated.

    Returns:
        ``retrieved`` with each id kept at its first rank only.

    """
    return list(dict.fromkeys(retrieved))


def _gain(rank: int) -> float:
    """Return the discounted gain of a relevant document at 1-based ``rank``.

    Args:
        rank: The zero-based rank.

    Returns:
        The discounted gain of a relevant document at 1-based ``rank``.

    """
    return 1.0 / math.log2(rank + 1)


def _score(
    metric: RankingMetric, relevant: frozenset[str], top: Sequence[str], k: int
) -> float:
    """Return ``metric``.

    For the top-``k`` ranking ``top`` against a non-empty ``relevant`` set.

    The ideal DCG ranks ``min(k, len(relevant))`` relevant documents first, however short
    ``top`` is, so a ranking that returns fewer than ``k`` documents is not rewarded.

    Args:
        metric: The ranking metric.
        relevant: The relevant ids.
        top: The top retrieved ids, best first.
        k: The cutoff.

    Returns:
        ``metric``.

    """
    hits = [rank for rank, doc in enumerate(top, start=1) if doc in relevant]
    if metric is RankingMetric.RECALL:
        return len(hits) / len(relevant)
    if metric is RankingMetric.RR:
        return 1.0 / hits[0] if hits else 0.0
    ideal = sum(_gain(rank) for rank in range(1, min(k, len(relevant)) + 1))
    return sum(_gain(rank) for rank in hits) / ideal


@final
class RankingGrader(Grader):
    """Scores one ``RankingMetric`` at a cutoff ``k``; named ``<metric>@<k>``."""

    def __init__(self, metric: RankingMetric, k: int, pass_score: float) -> None:
        """Bind the metric, the cutoff and the score a ranking must reach to pass.

        Args:
            metric: What to score.
            k: The cutoff, at least 1.
            pass_score: The bar in ``[0, 1]``; a score at or above it passes.

        """
        self._metric = metric
        self._k = k
        self._pass = pass_score

    @override
    async def grade(self, case: EvalCase, output: EvalOutput) -> GradeResult:
        """Score ``output.retrieved`` against ``case.expected["relevant"]``.

        A callee raises ``AppError``: ``INVALID_INPUT`` when the case's relevance
        judgments are unreadable.

        Args:
            case: The eval case the output answers.
            output: What the system under test produced for ``case``.

        Returns:
            The grade for this case.

        """
        name = f"{self._metric}@{self._k}"
        relevant = _relevant(case)
        top = _ranked(output.retrieved)[: self._k]
        if not relevant:
            score = 0.0 if top else 1.0
            why = "no relevant document: " + ("retrieved some" if top else "abstained")
            return GradeResult(name, score, passed=score >= self._pass, rationale=why)
        score = _score(self._metric, relevant, top, self._k)
        hits = sum(doc in relevant for doc in top)
        why = f"{hits} of {len(relevant)} relevant in the top {self._k}"
        return GradeResult(name, score, passed=score >= self._pass, rationale=why)
