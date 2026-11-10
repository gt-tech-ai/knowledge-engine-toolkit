"""Unit tests for the retrieval ranking graders (recall@k, nDCG@k, reciprocal rank).

The graders are pure: they compare a case's relevant ids with the ranking an output
    carries,
so no double is needed.
"""

from __future__ import annotations

import json
import math

import pytest

from techai_webutils.clients.grader import GraderConfig, GraderKind, grader_from_config
from techai_webutils.core.errors import AppError
from techai_webutils.core.interfaces.dataset import EvalCase
from techai_webutils.core.interfaces.grader import EvalOutput


def _case(relevant: list[str]) -> EvalCase:
    """Return a case whose relevant documents are ``relevant``.

    Args:
        relevant: The relevant document ids.

    Returns:
        A case whose relevant documents are ``relevant``.

    """
    return EvalCase(
        id="q-1",
        inputs={"query": "which document?"},
        expected={"relevant": json.dumps(relevant)},
        metadata={},
    )


async def _score(
    metric: str, k: int, relevant: list[str], retrieved: tuple[str, ...]
) -> float:
    """Grade ``retrieved`` for ``relevant`` with the ``metric@k`` ranking grader.

    Args:
        metric: The ranking metric.
        k: The cutoff.
        relevant: The relevant document ids.
        retrieved: The retrieved document ids, in order.

    Returns:
        The score.

    """
    grader = grader_from_config(GraderConfig(kind=GraderKind.RANKING, metric=metric, k=k))
    result = await grader.grade(
        _case(relevant), EvalOutput("", (), "", retrieved=retrieved)
    )
    assert result.grader == f"{metric}@{k}"
    return result.score


@pytest.mark.asyncio
async def test_metrics_match_hand_computed_values() -> None:
    """Test that recall@k, nDCG@k and reciprocal rank match values computed by hand.

    **Why this test is important:**
      - The eval gate compares these numbers between a baseline and a candidate config; an
        off-by-one rank, a duplicate counted twice or a wrong ideal DCG moves every
        verdict.

    **What it tests:**
      - Relevant {a, c} and the ranking b, a, a, d, c (the repeat of a counts once, so the
        ranks are b=1, a=2, d=3, c=4): recall@3 is 1/2 and recall@4 is 1; nDCG@3 is
        (1/log2 3) / (1 + 1/log2 3) and nDCG@4 is (1/log2 3 + 1/log2 5) / (1 + 1/log2 3);
        reciprocal rank is 1/2 at k=3 and 0 at k=1 (no relevant document in the top 1).
      - A ranking shorter than k is scored against the full ideal: relevant {a, c} and the
        ranking a alone give nDCG@10 = 1 / (1 + 1/log2 3), not 1.
      - A case with no relevant document scores 1 when nothing is retrieved (an
        abstention) and 0 otherwise.
    """
    ranking = ("b", "a", "a", "d", "c")
    relevant = ["a", "c"]
    ideal = 1 + 1 / math.log2(3)
    assert await _score("recall", 3, relevant, ranking) == pytest.approx(0.5)
    assert await _score("recall", 4, relevant, ranking) == pytest.approx(1.0)
    assert await _score("ndcg", 3, relevant, ranking) == pytest.approx(
        (1 / math.log2(3)) / ideal
    )
    assert await _score("ndcg", 4, relevant, ranking) == pytest.approx(
        (1 / math.log2(3) + 1 / math.log2(5)) / ideal
    )
    assert await _score("rr", 3, relevant, ranking) == pytest.approx(0.5)
    assert await _score("rr", 1, relevant, ranking) == pytest.approx(0.0)
    assert await _score("ndcg", 10, relevant, ("a",)) == pytest.approx(1 / ideal)
    assert await _score("ndcg", 10, [], ()) == pytest.approx(1.0)
    assert await _score("recall", 10, [], ("b",)) == pytest.approx(0.0)


@pytest.mark.parametrize(
    ("config", "why"),
    [
        (GraderConfig(kind=GraderKind.RANKING, metric="map", k=10), "metric"),
        (GraderConfig(kind=GraderKind.RANKING, metric="ndcg", k=0), "k"),
    ],
)
def test_ranking_grader_rejects_an_unknown_metric_or_a_cutoff_below_one(
    config: GraderConfig, why: str
) -> None:
    """Test that the factory refuses a ranking grader it cannot compute.

    **Why this test is important:**
      - A typo in an eval config would otherwise grade every case with a metric nobody
        chose, and a cutoff of 0 scores every ranking 0.

    **What it tests:**
      - An unknown metric and a cutoff below 1 each raise ``INVALID_INPUT`` naming it.

    Args:
        config: The grader's settings.
        why: The case's name.

    """
    with pytest.raises(AppError, match=why):
        _ = grader_from_config(config)


@pytest.mark.asyncio
async def test_ranking_grader_rejects_a_case_without_relevance_judgments() -> None:
    """Test that a case whose relevant list is missing or not a JSON list is refused.

    **Why this test is important:**
      - A case with unreadable judgments is a broken eval set, not a bad ranking; scoring
        it 0 would read as a retrieval regression.

    **What it tests:**
      - ``expected["relevant"]`` absent, or not a JSON list of strings, raises
        ``INVALID_INPUT``.
    """
    grader = grader_from_config(GraderConfig(kind=GraderKind.RANKING, metric="rr", k=5))
    for expected in ({}, {"relevant": "{}"}, {"relevant": "[1]"}):
        case = EvalCase(id="q", inputs={}, expected=expected, metadata={})
        with pytest.raises(AppError, match="relevant"):
            _ = await grader.grade(case, EvalOutput("", (), "", retrieved=("a",)))
