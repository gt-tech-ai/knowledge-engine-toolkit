"""Unit tests for retrieval abstention and passage order."""

from __future__ import annotations

from dataclasses import replace
from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.retrieval.abstain import AbstainerKind, abstention_policies
from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalEngineDependencies,
    RetrievalKind,
    new_retrieval_engine_from_config,
    wrap_retrieval_engine,
)
from techai_webutils.clients.retrieval.ordering import (
    PassageOrder,
    order_passages,
    passage_header,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult

_EXPECTED_SCORE = 0.5


def _passage(n: int, score: float) -> RetrievalResult:
    """Return passage ``n`` scoring ``score``.

    Args:
        n: The passage's number.
        score: Its score.

    Returns:
        Passage ``n`` scoring ``score``.

    """
    return RetrievalResult(
        document_id=f"d{n}",
        document_name=f"Doc {n}",
        chunk_content=f"text {n}",
        score=score,
        page_number=None,
        metadata={},
    )


def _engine(passages: list[RetrievalResult]) -> RetrievalEngine:
    """Return a mocked backend engine.

    (e.g. Bedrock with its reranker) returning ``passages``.

    Args:
        passages: The passages the engine returns.

    Returns:
        A mocked backend engine.

    """
    engine = MagicMock(spec=RetrievalEngine)
    engine.retrieve = AsyncMock(return_value=passages)
    return engine


@pytest.mark.asyncio
async def test_default_abstainer_is_score_threshold_0_5() -> None:
    """Test that the default config keeps today's 0.5 score floor on every engine.

    **Why this test is important:**
      - Abstaining on weak passages is what keeps the assistant from answering from noise;
        the refactor must not change it for anyone who did not ask.

    **What it tests:**
      - ``RetrievalConfig()`` has ``abstainer=score_threshold`` and ``min_score=0.5``; the
        stub engine drops a 0.49 passage and keeps a 0.5 one, in rank order
    """
    config = RetrievalConfig()
    engine = new_retrieval_engine_from_config(
        config,
        options=RetrievalEngineDependencies(
            policies=[],
            stub_passages=[_passage(1, 0.9), _passage(2, 0.49), _passage(3, 0.5)],
        ),
    )

    results = await engine.retrieve("text")

    assert (config.abstainer, config.min_score) == (AbstainerKind.SCORE_THRESHOLD, 0.5)
    assert config.passage_order is PassageOrder.RANK
    assert all(r.score >= _EXPECTED_SCORE for r in results)
    assert "d2" not in [r.document_id for r in results]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("abstainer", "min_score", "kept"),
    [
        (AbstainerKind.NONE, 0.5, ["d1", "d2", "d3"]),
        (AbstainerKind.SCORE_THRESHOLD, 0.7, ["d1"]),
        (AbstainerKind.SCORE_THRESHOLD, 0.2, ["d1", "d2"]),
    ],
)
async def test_abstainer_on_the_engines_scores(
    abstainer: AbstainerKind, min_score: float, kept: list[str]
) -> None:
    """Test that ``none`` keeps every candidate.

    And ``score_threshold`` drops below its value.

    **Why this test is important:**
      - The threshold reads whatever score the engine returns: a reranker's when reranking
        is on (a backend like Bedrock returns reranked scores), the retriever's otherwise.

    **What it tests:**
      - over an engine returning 0.9, 0.3, 0.1: ``none`` keeps all three; a 0.7 threshold
        keeps one; a 0.2 threshold keeps two

    Args:
        abstainer: Decides when to abstain.
        min_score: The lowest score kept.
        kept: The passages kept.

    """
    config = RetrievalConfig(abstainer=abstainer, min_score=min_score)
    inner = _engine([_passage(1, 0.9), _passage(2, 0.3), _passage(3, 0.1)])

    results = await wrap_retrieval_engine(inner, config, policies=[]).retrieve("q")

    assert [r.document_id for r in results] == kept


@pytest.mark.asyncio
async def test_edges_order_puts_the_best_at_both_ends() -> None:
    """Test that ``edges`` orders ranks 1, 3, 5 … 4, 2, through the factory's wrapper.

    **Why this test is important:**
      - Models attend least to the middle of a long context; the weakest passages belong
        there.

    **What it tests:**
      - five passages ranked 1-5 come back as 1, 3, 5, 4, 2; ``rank`` keeps them as they
        are; an empty list stays empty
    """
    passages = [_passage(n, 1.0 - n / 10) for n in range(1, 6)]
    config = RetrievalConfig(
        abstainer=AbstainerKind.NONE, passage_order=PassageOrder.EDGES
    )

    edges = await wrap_retrieval_engine(_engine(passages), config, policies=[]).retrieve(
        "q"
    )

    assert [r.document_id for r in edges] == ["d1", "d3", "d5", "d4", "d2"]
    assert order_passages(passages, PassageOrder.RANK) == passages
    assert order_passages([], PassageOrder.EDGES) == []


def test_passage_header_names_document_section_and_page() -> None:
    """Test that a passage header carries its number, document, section and page.

    **Why this test is important:**
      - The model cites ``[n]``; the header tells it (and a reader of the prompt) where
        the passage comes from.

    **What it tests:**
      - a passage with a section and page reads ``[2] Doc 1 · Intro · p. 4``; one without
        them reads ``[1] Doc 1``
    """
    bare = _passage(1, 0.9)
    full = RetrievalResult(
        document_id="d1",
        document_name="Doc 1",
        chunk_content="t",
        score=0.9,
        page_number=4,
        metadata={"section_path": "Intro"},
    )

    assert passage_header(2, full) == "[2] Doc 1 · Intro · p. 4"
    assert passage_header(1, bare) == "[1] Doc 1"


def test_unknown_abstainer_and_order_are_refused() -> None:
    """Test that an unknown abstainer or passage order fails loudly.

    **Why this test is important:**
      - A typo in config must stop the app, not silently disable abstention.

    **What it tests:**
      - both raise ``AppValueError``; the opensearch kind without a store raises too
    """
    with pytest.raises(AppValueError):
        _ = abstention_policies(cast("AbstainerKind", cast("object", "bogus")), 0.5)
    with pytest.raises(AppValueError):
        _ = order_passages([], cast("PassageOrder", cast("object", "bogus")))
    with pytest.raises(Exception, match="opensearch"):
        _ = new_retrieval_engine_from_config(
            RetrievalConfig(kind=RetrievalKind.OPENSEARCH),
            options=RetrievalEngineDependencies(policies=[]),
        )


@pytest.mark.asyncio
async def test_top_score_abstainer_abstains_only_when_the_best_passage_is_weak() -> None:
    """Test that ``top_score`` returns nothing.

    When the best passage is below the threshold.

    **Why this test is important:**
      - A calibrated threshold answers "is anything here good enough?", not "which
        passages clear a floor": a fused or reranked pool is kept whole when its best
        passage is strong, and the LLM call is skipped when it is not, whatever the score
        scale.

    **What it tests:**
      - At threshold 0.6: a pool whose best is 0.7 keeps every passage (0.1 included), a
        pool whose best is 0.5 is empty, and under ``edges`` order the pool is still kept
        and reordered.
      - ``top_score`` with a threshold of 0 or less is refused.
    """
    config = RetrievalConfig(abstainer=AbstainerKind.TOP_SCORE, abstain_threshold=0.6)
    strong = _engine([_passage(1, 0.7), _passage(2, 0.1)])
    weak = _engine([_passage(1, 0.5), _passage(2, 0.4)])
    edges = replace(config, passage_order=PassageOrder.EDGES)
    three = _engine([_passage(1, 0.7), _passage(2, 0.3), _passage(3, 0.2)])

    kept = await wrap_retrieval_engine(strong, config, policies=[]).retrieve("q")
    dropped = await wrap_retrieval_engine(weak, config, policies=[]).retrieve("q")
    ordered = await wrap_retrieval_engine(three, edges, policies=[]).retrieve("q")

    assert [r.document_id for r in kept] == ["d1", "d2"]
    assert dropped == []
    assert [r.document_id for r in ordered] == ["d1", "d3", "d2"]
    with pytest.raises(AppValueError, match="abstain_threshold"):
        _ = RetrievalConfig(abstainer=AbstainerKind.TOP_SCORE)
