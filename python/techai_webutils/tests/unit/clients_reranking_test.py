"""Unit tests for the reranker factory and cross-encoder reranking.

Of retrieved passages.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.reranking import (
    RerankerConfig,
    RerankerKind,
    reranker_from_config,
)
from techai_webutils.clients.retrieval.abstain import AbstainerKind
from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalEngineDependencies,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.retrieval.reranking import RerankingKind
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.reranker import RerankClient, Reranker
from techai_webutils.core.interfaces.retrieval import RetrievalResult

if TYPE_CHECKING:
    from collections.abc import Sequence


def _passage(n: int, score: float) -> RetrievalResult:
    """Return passage ``n`` with text ``text n`` scoring ``score``.

    Args:
        n: The passage's number.
        score: Its score.

    Returns:
        Passage ``n`` with text ``text n`` scoring ``score``.

    """
    return RetrievalResult(
        document_id=f"d{n}",
        document_name=f"Doc {n}",
        chunk_content=f"text {n}",
        score=score,
        page_number=None,
        metadata={},
    )


def _client(scores: dict[str, float]) -> RerankClient:
    """Return a mocked rerank transport scoring each passage text from ``scores``.

    Args:
        scores: Each passage's rerank score.

    Returns:
        A mocked rerank transport scoring each passage text from ``scores``.

    """

    def score(_query: str, passages: Sequence[str]) -> list[float]:
        return [scores[p] for p in passages]

    client = MagicMock()
    client.score = AsyncMock(side_effect=score)
    return cast("RerankClient", client)


@pytest.mark.asyncio
async def test_cross_encoder_reorders_passages_by_the_mocked_scores() -> None:
    """Test that ``cross_encoder`` reranking reorders any engine's passages.

    And sets their scores.

    **Why this test is important:**
      - Off Bedrock there is no managed reranker; a cross-encoder is what puts the passage
        that answers the question first, so its scores must decide the order.

    **What it tests:**
      - the stub engine returns d1, d2, d3 (0.9, 0.8, 0.7); the cross-encoder scores them
        0.1, 0.95, 0.6, so the result is d2, d3, d1 with those scores
      - the scores are sent in batches of two (two calls for three passages)
      - the 0.5 abstainer reads the reranker's scores, dropping d1
    """
    client = _client({"text 1": 0.1, "text 2": 0.95, "text 3": 0.6})
    reranker = reranker_from_config(
        RerankerConfig(kind=RerankerKind.CROSS_ENCODER, batch_size=2), client=client
    )
    engine = new_retrieval_engine_from_config(
        RetrievalConfig(reranking_kind=RerankingKind.CROSS_ENCODER, min_score=0.0),
        options=RetrievalEngineDependencies(
            policies=[],
            stub_passages=[_passage(1, 0.9), _passage(2, 0.8), _passage(3, 0.7)],
            reranker=reranker,
        ),
    )
    thresholded = new_retrieval_engine_from_config(
        RetrievalConfig(reranking_kind="cross_encoder"),
        options=RetrievalEngineDependencies(
            policies=[],
            stub_passages=[_passage(1, 0.9), _passage(2, 0.8), _passage(3, 0.7)],
            reranker=reranker,
        ),
    )

    results = await engine.retrieve("text")
    kept = await thresholded.retrieve("text")

    assert [(r.document_id, r.score) for r in results] == [
        ("d2", 0.95),
        ("d3", 0.6),
        ("d1", 0.1),
    ]
    assert [r.document_id for r in kept] == ["d2", "d3"]
    first, second = cast("AsyncMock", client.score).await_args_list[:2]
    assert first.args == ("text", ["text 1", "text 2"])
    assert second.args == ("text", ["text 3"])


@pytest.mark.asyncio
async def test_noop_reranker_keeps_order_and_empty_results_skip_the_reranker() -> None:
    """Test that the default reranker keeps the input order and no passages means no call.

    **Why this test is important:**
      - ``noop`` is the factory's zero-infra default; it must not shuffle passages, and an
        empty retrieval must not pay for a model call.

    **What it tests:**
      - ``noop`` scores three passages 1.0, 2/3, 1/3 and none as ``[]``
      - a reranking engine over an empty result never calls its reranker
    """
    noop = reranker_from_config(RerankerConfig())
    unused = MagicMock(spec=Reranker)
    unused.rerank = AsyncMock()
    engine = new_retrieval_engine_from_config(
        RetrievalConfig(reranking_kind="cross_encoder", abstainer=AbstainerKind.NONE),
        options=RetrievalEngineDependencies(policies=[], reranker=unused),
    )

    scores = await noop.rerank("q", ["a", "b", "c"])

    assert scores == pytest.approx([1.0, 2 / 3, 1 / 3])
    assert await noop.rerank("q", []) == []
    assert await engine.retrieve("q") == []
    cast("MagicMock", cast("AsyncMock", unused.rerank).assert_not_awaited)()


@pytest.mark.asyncio
async def test_a_short_score_list_is_an_internal_error() -> None:
    """Test that a client returning the wrong number of scores fails loudly.

    **Why this test is important:**
      - A score list shorter than the passages would pair scores with the wrong passages
        and cite the wrong source.

    **What it tests:**
      - a client returning one score for two passages raises ``InternalError``
    """
    client = MagicMock()
    client.score = AsyncMock(return_value=[0.5])
    reranker = reranker_from_config(
        RerankerConfig(kind=RerankerKind.CROSS_ENCODER),
        client=cast("RerankClient", client),
    )

    with pytest.raises(AppError) as raised:
        _ = await reranker.rerank("q", ["a", "b"])

    assert raised.value.code is ErrorCode.INTERNAL


def test_misconfigured_reranking_is_refused() -> None:
    """Test that every reranking misconfiguration fails at construction.

    With ``INVALID_INPUT``.

    **Why this test is important:**
      - ``bedrock_rerank`` reranks inside Bedrock Retrieve; on a vector engine it would
        silently do nothing, so a deployment leaving Bedrock must be told to pick
        ``cross_encoder``.

    **What it tests:**
      - ``bedrock_rerank`` on the ``opensearch`` kind; ``cross_encoder`` without a
        reranker; an unknown reranking kind; ``cross_encoder`` reranker without a client,
        with batch size 0, or an unknown reranker kind — each raises ``AppValueError``
    """
    with pytest.raises(AppValueError, match="bedrock_rerank"):
        _ = new_retrieval_engine_from_config(
            RetrievalConfig(
                kind=RetrievalKind.OPENSEARCH, reranking_kind="bedrock_rerank"
            ),
            options=RetrievalEngineDependencies(policies=[]),
        )
    with pytest.raises(AppValueError, match="reranker"):
        _ = new_retrieval_engine_from_config(
            RetrievalConfig(reranking_kind="cross_encoder"),
            options=RetrievalEngineDependencies(policies=[]),
        )
    with pytest.raises(AppValueError, match="unknown reranking kind"):
        _ = RetrievalConfig(reranking_kind="cohere")
    with pytest.raises(AppValueError, match="client"):
        _ = reranker_from_config(RerankerConfig(kind=RerankerKind.CROSS_ENCODER))
    with pytest.raises(AppValueError, match="batch_size"):
        _ = reranker_from_config(
            RerankerConfig(kind=RerankerKind.CROSS_ENCODER, batch_size=0),
            client=_client({}),
        )
    with pytest.raises(AppValueError, match="unknown reranker kind"):
        _ = reranker_from_config(
            RerankerConfig(kind=cast("RerankerKind", cast("object", "bogus")))
        )
