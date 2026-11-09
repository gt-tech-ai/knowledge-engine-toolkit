"""Unit tests for collapsing duplicate passages (``clients.retrieval.collapse``)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    wrap_retrieval_engine,
)
from techai_webutils.clients.retrieval.collapse import ALSO_IN_KEY
from techai_webutils.clients.retrieval.filtering import MetadataEquals
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult


def _passage(doc: str, text: str, score: float, **metadata: str) -> RetrievalResult:
    """Return a passage of ``doc`` holding ``text``.

    Returns:
        A passage of ``doc`` holding ``text``.

    """
    return RetrievalResult(
        document_id=doc,
        document_name=doc.upper(),
        chunk_content=text,
        score=score,
        page_number=None,
        metadata={"scope": "a", **metadata},
    )


def _engine(passages: list[RetrievalResult]) -> RetrievalEngine:
    """Return a mocked backend engine answering ``passages``.

    Returns:
        A mocked backend engine answering ``passages``.

    """
    engine = MagicMock(spec=RetrievalEngine)
    engine.retrieve = AsyncMock(return_value=passages)
    return engine


@pytest.mark.asyncio
async def test_duplicate_passages_collapse_to_the_best_scored_member() -> None:
    """Test that copies of one chunk collapse into the best-scored one.

    Which names the others.

    **Why this test is important:**
      - The same file uploaded three times fills the top k with one passage three times,
        crowding out everything else; collapsing keeps one and still tells the reader
        where else the text lives. A copy outside the caller's scope must never be named.

    **What it tests:**
      - Three passages with the same text up to case and whitespace collapse into the 0.9
        one, whose ``also_in`` lists the other two documents; a passage sharing a cluster
        id (near-duplicate text) joins it too; a copy in another scope is dropped by the
        scope policy first and never named; distinct text is kept; off by default.
    """
    passages = [
        _passage("d2", "Revenue grew 18%.", 0.7),
        _passage("d1", "revenue  grew 18%.", 0.9),
        _passage("d3", "Revenue grew 18%.", 0.5),
        _passage("d4", "Revenue rose by 18 percent.", 0.6, cluster="k1"),
        _passage("d5", "Revenue grew 18 %.", 0.4, cluster="k1"),
        _passage("dx", "Revenue grew 18%.", 0.95, scope="b"),
        _passage("d6", "Costs fell.", 0.3),
    ]
    config = RetrievalConfig(
        min_score=0.0, collapse_duplicates=True, collapse_cluster_key="cluster"
    )
    scope = [MetadataEquals("scope")]

    results = await wrap_retrieval_engine(
        _engine(passages), config, policies=scope
    ).retrieve("q", filters={"scope": "a"})
    plain = await wrap_retrieval_engine(
        _engine(passages), RetrievalConfig(min_score=0.0), policies=scope
    ).retrieve("q", filters={"scope": "a"})

    assert [(r.document_id, r.metadata.get(ALSO_IN_KEY, "")) for r in results] == [
        ("d1", "d2,d3"),
        ("d4", "d5"),
        ("d6", ""),
    ]
    assert len(plain) == 6
