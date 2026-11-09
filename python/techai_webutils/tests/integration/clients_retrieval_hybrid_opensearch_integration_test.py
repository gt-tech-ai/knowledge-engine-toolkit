"""Integration test: hybrid k-NN + BM25 retrieval over a real OpenSearch (testcontainers).

The embedder is mocked (the dependency under test is OpenSearch); the store, its keyword
index and the fusion are real.
"""

from __future__ import annotations

import uuid
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest
import pytest_asyncio
from opensearchpy import AsyncOpenSearch

from techai_webutils.clients.retrieval.abstain import AbstainerKind
from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalEngineDependencies,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.vector.opensearch import (
    OpenSearchConfig,
    OpenSearchVectorStore,
)
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.vector_store import VectorEntry

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

pytestmark = pytest.mark.integration


@pytest_asyncio.fixture
async def client(opensearch_url: str) -> AsyncIterator[AsyncOpenSearch]:
    """Yield a client on the session's OpenSearch.

    Yields:
        A client on the session's OpenSearch.

    """
    client = AsyncOpenSearch(hosts=[opensearch_url])
    try:
        yield client
    finally:
        await client.close()


def _embedder(vector: list[float]) -> EmbeddingProvider:
    """Return an embedder whose every query embeds to ``vector``.

    Returns:
        An embedder whose every query embeds to ``vector``.

    """
    embedder = MagicMock(spec=EmbeddingProvider)
    embedder.dimension.return_value = len(vector)
    embedder.embed = AsyncMock(
        return_value=EmbeddingResult(embedding=vector, model="m", token_count=0)
    )
    return embedder


@pytest.mark.asyncio
async def test_hybrid_returns_the_lexical_and_the_semantic_match(
    client: AsyncOpenSearch,
) -> None:
    """Test that one query finds a document by its exact term and another by its vector.

    Fused.

    **Why this test is important:**
      - A rare term (a product code, a name) has no useful embedding, and a paraphrase
        shares no term; only hybrid retrieval returns both, which is the reason to move
        off pure k-NN.

    **What it tests:**
      - ``lex`` holds the word ``zephyrine`` with a far vector; ``sem`` has the query's
        vector and no shared word; ``far`` matches neither. The ``opensearch`` engine
        (RRF, abstention off) returns ``lex`` and ``sem`` before ``far``
    """
    collection = f"c{uuid.uuid4().hex[:10]}"
    store = OpenSearchVectorStore(
        client, dimension=4, config=OpenSearchConfig(refresh_on_write=True)
    )
    entries = [
        VectorEntry(
            "lex:0",
            "lex",
            "the zephyrine valve spec",
            [0.0, 0.0, 1.0, 0.0],
            {"org_id": "o"},
        ),
        VectorEntry(
            "sem:0", "sem", "how to fix a pump", [1.0, 0.0, 0.0, 0.0], {"org_id": "o"}
        ),
        VectorEntry(
            "far:0",
            "far",
            "quarterly sales numbers",
            [0.0, 1.0, 0.0, 0.0],
            {"org_id": "o"},
        ),
    ]
    await store.upsert(collection, entries)
    engine = new_retrieval_engine_from_config(
        RetrievalConfig(
            kind=RetrievalKind.OPENSEARCH,
            vector_collection=collection,
            abstainer=AbstainerKind.NONE,
        ),
        options=RetrievalEngineDependencies(
            policies=[],
            store_filter_keys=["org_id"],
            embedder=_embedder([1.0, 0.0, 0.0, 0.0]),
            store=store,
        ),
    )

    results = await engine.retrieve("zephyrine", top_k=3, filters={"org_id": "o"})

    ids = [r.document_id for r in results]
    assert set(ids[:2]) == {"lex", "sem"}
    assert results[0].score > 0
