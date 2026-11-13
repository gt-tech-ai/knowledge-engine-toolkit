"""Tests for the vector-backed RetrievalEngine.

They also cover the qdrant factory branch; deps are mocked.
"""

from __future__ import annotations

from typing import cast
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from techai_webutils.clients.retrieval.abstain import AbstainerKind
from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalEngineDependencies,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.retrieval.filtering import (
    FilteringRetrievalEngine,
    MetadataEquals,
    MinScore,
)
from techai_webutils.clients.retrieval.vector import VectorRetrievalEngine
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.retrieval import FilterIn
from techai_webutils.core.interfaces.vector_store import VectorSearchResult, VectorStore

_EXPECTED_KWARGS = 5
_EXPECTED_PAGE_NUMBER = 7


def _embedder(dimension: int = 3) -> EmbeddingProvider:
    """Mock EmbeddingProvider returning a fixed vector of the given dimension.

    Args:
        dimension: The vector size.

    Returns:
        The mock embedder.

    """
    embedder = MagicMock(spec=EmbeddingProvider)
    cast("MagicMock", embedder.dimension).return_value = dimension
    embedder.embed = AsyncMock(
        return_value=EmbeddingResult(embedding=[0.1, 0.2, 0.3], model="m", token_count=0)
    )
    return embedder


def _store(
    hits: list[VectorSearchResult],
    dimension: int = 3,
    keyword_hits: list[VectorSearchResult] | None = None,
) -> VectorStore:
    """Mock VectorStore returning fixed hits.

    With ``keyword_hits`` it also has keyword search.

    Args:
        hits: The vector hits.
        dimension: The vector size.
        keyword_hits: The keyword hits.

    Returns:
        The mock store.

    """
    store = MagicMock(spec=VectorStore)
    store.dimension = dimension
    store.search = AsyncMock(return_value=hits)
    store.supports_keyword = keyword_hits is not None
    store.keyword_search = AsyncMock(return_value=keyword_hits or [])
    return store


def _hit_of(chunk_id: str, score: float, **metadata: str) -> VectorSearchResult:
    """Return a hit on chunk ``chunk_id`` with ``score``.

    Args:
        chunk_id: The chunk.
        score: Its score.
        **metadata: The hit's metadata.

    Returns:
        A hit on chunk ``chunk_id`` with ``score``.

    """
    return VectorSearchResult(
        document_id=chunk_id.split(":", maxsplit=1)[0],
        chunk_id=chunk_id,
        content=f"text {chunk_id}",
        score=score,
        metadata=dict(metadata),
    )


@pytest.mark.asyncio
async def test_hybrid_store_fuses_knn_and_keyword_rankings() -> None:
    """Test that a store with keyword search gets both searches, fused by RRF.

    **Why this test is important:**
      - Exact terms (codes, names) are found by BM25 and paraphrases by k-NN; hybrid
        retrieval must return both, ranked by one fused order.

    **What it tests:**
      - the k-NN and keyword searches both run with the pushed-down filters and the
        call's ``where``
      - ``a`` (first in both) leads, ``b`` (k-NN only) and ``c`` (keyword only) follow;
        scores are the RRF values
    """
    store = _store(
        [_hit_of("a:0", 0.9), _hit_of("b:0", 0.8)],
        keyword_hits=[_hit_of("a:0", 12.0), _hit_of("c:0", 9.0)],
    )
    engine = VectorRetrievalEngine(_embedder(), store, "docs", filter_keys=["org_id"])

    where = FilterIn("tags", ("api",))

    results = await engine.retrieve(
        "q", top_k=3, filters={"org_id": "o1", "x": "y"}, where=where
    )

    cast("AsyncMock", store.search).assert_awaited_once_with(
        "docs", [0.1, 0.2, 0.3], top_k=3, filters={"org_id": "o1"}, where=where
    )
    cast("AsyncMock", store.keyword_search).assert_awaited_once_with(
        "docs", "q", 3, {"org_id": "o1"}, where
    )
    assert [r.document_id for r in results] == ["a", "b", "c"]
    assert results[0].score == pytest.approx(2 / 61)
    assert results[1].score == pytest.approx(1 / 62)


@pytest.mark.asyncio
async def test_opensearch_kind_builds_the_hybrid_engine_with_abstention_off() -> None:
    """Test that the ``opensearch`` kind builds the vector engine.

    ``abstainer=none`` keeps RRF scores.

    **Why this test is important:**
      - Fused RRF scores are about 0.03; the default 0.5 floor would drop every passage,
        so a hybrid deployment selects ``none``.

    **What it tests:**
      - with ``abstainer=none`` both fused passages survive; with the default they are all
        dropped
    """
    store = _store([_hit_of("a:0", 0.9)], keyword_hits=[_hit_of("b:0", 3.0)])
    config = RetrievalConfig(kind=RetrievalKind.OPENSEARCH, abstainer=AbstainerKind.NONE)

    kept = await new_retrieval_engine_from_config(
        config,
        options=RetrievalEngineDependencies(
            policies=[], embedder=_embedder(), store=store
        ),
    ).retrieve("q")
    dropped = await new_retrieval_engine_from_config(
        RetrievalConfig(kind=RetrievalKind.OPENSEARCH),
        options=RetrievalEngineDependencies(
            policies=[], embedder=_embedder(), store=store
        ),
    ).retrieve("q")

    assert [r.document_id for r in kept] == ["a", "b"]
    assert dropped == []


def _hit() -> VectorSearchResult:
    """Return a search hit carrying a scope key, a document name and a page.

    Returns:
        A search hit carrying a scope key, a document name and a page.

    """
    return VectorSearchResult(
        document_id="doc-1",
        chunk_id="doc-1:0",
        content="the retrieved chunk",
        score=0.88,
        metadata={
            "tenant": "t1",
            "document_name": "Annual Report",
            "page_number": "7",
        },
    )


class TestVectorRetrievalEngine:
    """Tests for the vector retrieval engine."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_retrieve_pushes_down_only_the_named_filters() -> None:
        """retrieve() embeds the query, then searches.

        It passes only the filters named in filter_keys.

        Why this test is important:
          - A request filter that is not a stored payload field (e.g. a caller level)
            matches no points when pushed down, so pushing every filter would return
            nothing; only payload keys go down.

        What it tests:
          - embed is called with the query as a query; store.search gets the top_k and
            exactly {"tenant": ...} for a request carrying tenant + max_level with
            filter_keys=("tenant",).
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = VectorRetrievalEngine(
            embedder, store, collection="documents", filter_keys=("tenant",)
        )

        _ = await engine.retrieve(
            "annual revenue", top_k=5, filters={"tenant": "t1", "max_level": "high"}
        )

        embedder.embed.assert_awaited_once_with("annual revenue", input_type="query")
        store.search.assert_awaited_once()
        assert store.search.call_args.kwargs["filters"] == {"tenant": "t1"}
        assert store.search.call_args.kwargs["top_k"] == _EXPECTED_KWARGS

    @staticmethod
    @pytest.mark.asyncio
    async def test_retrieve_maps_all_metadata_and_survives_filter() -> None:
        """The mapped passage keeps its metadata, name and page.

        It survives a scope policy.

        Why this test is important:
          - A MetadataEquals scope policy drops any passage whose scope key does not match
            the request; if the vector engine dropped that key, every real passage would
            vanish behind the decorator.

        What it tests:
          - The RetrievalResult carries the tenant key, document_name and an int
            page_number, and survives MinScore(0.5) + MetadataEquals("tenant").
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = FilteringRetrievalEngine(
            VectorRetrievalEngine(embedder, store, "documents"),
            [MinScore(0.5), MetadataEquals("tenant")],
        )

        results = await engine.retrieve("q", top_k=5, filters={"tenant": "t1"})

        assert len(results) == 1
        r = results[0]
        assert r.document_id == "doc-1"
        assert r.document_name == "Annual Report"
        assert r.page_number == _EXPECTED_PAGE_NUMBER
        assert r.chunk_content == "the retrieved chunk"
        assert r.metadata["tenant"] == "t1"

    @staticmethod
    @pytest.mark.asyncio
    async def test_retrieve_warns_when_index_id_names_another_collection() -> None:
        """A per-call index_id the engine cannot honour is logged.

        It is not silently dropped.

        Why this test is important:
          - The interface treats index_id as routing (e.g. an index per tenant). This
            engine reads one collection, so a consumer relying on index_id for isolation
            would otherwise query the shared collection with no signal at all.

        What it tests:
          - retrieve(index_id="tenant-b-index") still searches "documents" and logs a
            warning naming both; index_id equal to the collection (or None) logs nothing.
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = VectorRetrievalEngine(embedder, store, collection="documents")

        with patch(
            "techai_webutils.clients.retrieval.vector.engine.logger"
        ) as mock_logger:
            _ = await engine.retrieve("q", index_id="documents")
            _ = await engine.retrieve("q")
            cast("MagicMock", cast("MagicMock", mock_logger.warning).assert_not_called)()
            _ = await engine.retrieve("q", index_id="tenant-b-index")

        cast("MagicMock", cast("MagicMock", mock_logger.warning).assert_called_once_with)(
            "vector retrieval ignores a per-call index_id; it reads one collection",
            index_id="tenant-b-index",
            collection="documents",
        )
        assert store.search.call_args.args[0] == "documents"

    @staticmethod
    def test_dimension_mismatch_fails_loudly() -> None:
        """A drifted embedder/store dimension raises at construction.

        There is a single source of truth.
        """
        with pytest.raises(AppValueError, match="dimension"):
            _ = VectorRetrievalEngine(
                _embedder(dimension=768), _store([], dimension=3), "documents"
            )


class TestQdrantFactoryBranch:
    """Tests for the Qdrant factory branch."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_from_config_qdrant_pushes_the_store_filter_keys_down() -> None:
        """The qdrant factory branch builds a filtered vector engine.

        It pushes store_filter_keys down.

        Why this test is important:
          - store_filter_keys is the only way a factory-built vector engine pushes a scope
            to Qdrant; if the factory stopped forwarding it, the index-wide top_k would be
            post-filtered and a scope would lose its passages with the suite still green.

        What it tests:
          - With store_filter_keys=("tenant",) and a request carrying tenant + max_level,
            the store is searched in the configured collection with exactly {"tenant":
            "t1"}, and the passage survives the MetadataEquals("tenant") policy, using the
            injected embedder and store.
        """
        embedder, store = _embedder(dimension=768), _store([_hit()], dimension=768)
        config = RetrievalConfig(
            kind=RetrievalKind.QDRANT,
            min_score=0.0,
            vector_collection="documents",
            embedding_model="nomic-embed-text",
        )
        engine = new_retrieval_engine_from_config(
            config,
            options=RetrievalEngineDependencies(
                policies=[MetadataEquals("tenant")],
                store_filter_keys=("tenant",),
                embedder=embedder,
                store=store,
            ),
        )

        results = await engine.retrieve(
            "q", filters={"tenant": "t1", "max_level": "high"}
        )

        assert isinstance(engine, FilteringRetrievalEngine)
        assert store.search.call_args.args[0] == "documents"
        assert store.search.call_args.kwargs["filters"] == {"tenant": "t1"}
        assert [r.document_id for r in results] == ["doc-1"]

    @staticmethod
    @pytest.mark.parametrize("missing", ["embedder", "store"])
    def test_from_config_qdrant_refuses_a_missing_embedder_or_store(missing: str) -> None:
        """The qdrant factory branch fails loudly without the embedder or store.

        That is, when the caller omits either.

        Why this test is important:
          - The factory no longer builds the pair itself (a client family never imports a
            sibling), so a composition root that forgets one must fail at startup with a
            coded error, not on the first query with an attribute error.

        What it tests:
          - kind=qdrant with only one of embedder/store supplied raises
            AppError(INVALID_INPUT).

        Args:
            missing: The collaborator left out.

        """
        embedder = None if missing == "embedder" else _embedder()
        store = None if missing == "store" else _store([])
        with pytest.raises(AppError) as raised:
            _ = new_retrieval_engine_from_config(
                RetrievalConfig(kind=RetrievalKind.QDRANT),
                options=RetrievalEngineDependencies(
                    policies=[], embedder=embedder, store=store
                ),
            )
        assert raised.value.code is ErrorCode.INVALID_INPUT
