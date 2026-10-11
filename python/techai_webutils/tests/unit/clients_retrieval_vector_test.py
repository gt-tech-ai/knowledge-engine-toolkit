"""Tests for the vector-backed RetrievalEngine + the qdrant factory branch (mocked deps)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
)
from techai_webutils.clients.retrieval.filtering import (
    FilteringRetrievalEngine,
    MetadataEquals,
    MinScore,
)
from techai_webutils.clients.retrieval.vector import VectorRetrievalEngine
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.vector_store import VectorSearchResult, VectorStore


def _embedder(dimension: int = 3) -> EmbeddingProvider:
    """Mock EmbeddingProvider returning a fixed vector of the given dimension."""
    embedder = MagicMock(spec=EmbeddingProvider)
    embedder.dimension.return_value = dimension
    embedder.embed = AsyncMock(
        return_value=EmbeddingResult(embedding=[0.1, 0.2, 0.3], model="m", token_count=0)
    )
    return embedder


def _store(hits: list[VectorSearchResult], dimension: int = 3) -> VectorStore:
    """Mock VectorStore returning fixed hits with a matching dimension."""
    store = MagicMock(spec=VectorStore)
    store.dimension = dimension
    store.search = AsyncMock(return_value=hits)
    return store


def _hit() -> VectorSearchResult:
    """Return a search hit carrying a scope key, a document name and a page."""
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

    @pytest.mark.asyncio
    async def test_retrieve_pushes_down_only_the_named_filters(self) -> None:
        """retrieve() embeds the query, then searches with only the filters named in filter_keys.

        Why this test is important:
          - A request filter that is not a stored payload field (e.g. a caller level) matches no points
            when pushed down, so pushing every filter would return nothing; only payload keys go down.

        What it tests:
          - embed is called with the query; store.search gets the top_k and exactly {"tenant": ...} for a
            request carrying tenant + max_level with filter_keys=("tenant",).
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = VectorRetrievalEngine(
            embedder, store, collection="documents", filter_keys=("tenant",)
        )

        await engine.retrieve(
            "annual revenue", top_k=5, filters={"tenant": "t1", "max_level": "high"}
        )

        embedder.embed.assert_awaited_once_with("annual revenue")
        store.search.assert_awaited_once()
        assert store.search.call_args.kwargs["filters"] == {"tenant": "t1"}
        assert store.search.call_args.kwargs["top_k"] == 5

    @pytest.mark.asyncio
    async def test_retrieve_maps_all_metadata_and_survives_filter(self) -> None:
        """The mapped passage keeps its metadata, name and page, and survives a scope policy.

        Why this test is important:
          - A MetadataEquals scope policy drops any passage whose scope key does not match the request; if
            the vector engine dropped that key, every real passage would vanish behind the decorator.

        What it tests:
          - The RetrievalResult carries the tenant key, document_name and an int page_number, and survives
            MinScore(0.5) + MetadataEquals("tenant").
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
        assert r.page_number == 7
        assert r.chunk_content == "the retrieved chunk"
        assert r.metadata["tenant"] == "t1"

    @pytest.mark.asyncio
    async def test_retrieve_warns_when_index_id_names_another_collection(self) -> None:
        """A per-call index_id the single-collection engine cannot honour is logged, not silently dropped.

        Why this test is important:
          - The interface treats index_id as routing (e.g. an index per tenant). This engine reads one
            collection, so a consumer relying on index_id for isolation would otherwise query the shared
            collection with no signal at all.

        What it tests:
          - retrieve(index_id="tenant-b-index") still searches "documents" and logs a warning naming both;
            index_id equal to the collection (or None) logs nothing.
        """
        embedder, store = _embedder(), _store([_hit()])
        engine = VectorRetrievalEngine(embedder, store, collection="documents")

        with patch(
            "techai_webutils.clients.retrieval.vector.engine.logger"
        ) as mock_logger:
            await engine.retrieve("q", index_id="documents")
            await engine.retrieve("q")
            mock_logger.warning.assert_not_called()
            await engine.retrieve("q", index_id="tenant-b-index")

        mock_logger.warning.assert_called_once_with(
            "vector retrieval ignores a per-call index_id; it reads one collection",
            index_id="tenant-b-index",
            collection="documents",
        )
        assert store.search.call_args.args[0] == "documents"

    def test_dimension_mismatch_fails_loudly(self) -> None:
        """A drifted embedder/store dimension raises at construction (single source of truth)."""
        with pytest.raises(ValueError, match="dimension"):
            VectorRetrievalEngine(
                _embedder(dimension=768), _store([], dimension=3), "documents"
            )


class TestQdrantFactoryBranch:
    """Tests for the Qdrant factory branch."""

    @pytest.mark.asyncio
    async def test_from_config_qdrant_pushes_the_store_filter_keys_down(self) -> None:
        """The qdrant factory branch builds a filtered vector engine that pushes store_filter_keys down.

        Why this test is important:
          - store_filter_keys is the only way a factory-built vector engine pushes a scope to Qdrant; if
            the factory stopped forwarding it, the index-wide top_k would be post-filtered and a scope
            would lose its passages with the suite still green.

        What it tests:
          - With store_filter_keys=("tenant",) and a request carrying tenant + max_level, the store is
            searched in the configured collection with exactly {"tenant": "t1"}, and the passage survives
            the MetadataEquals("tenant") policy.
        """
        embedder, store = _embedder(dimension=768), _store([_hit()], dimension=768)
        config = RetrievalConfig(
            kind=RetrievalKind.QDRANT,
            min_score=0.0,
            vector_url="http://qdrant:6333",
            vector_collection="documents",
            embedding_host="http://ollama:11434",
            embedding_model="nomic-embed-text",
            embedding_dimension=768,
        )
        with patch(
            "techai_webutils.clients.vector.composition.build_embedder_and_store",
            return_value=(embedder, store),
        ) as build:
            engine = new_retrieval_engine_from_config(
                config, policies=[MetadataEquals("tenant")], store_filter_keys=("tenant",)
            )

        results = await engine.retrieve(
            "q", filters={"tenant": "t1", "max_level": "high"}
        )

        assert isinstance(engine, FilteringRetrievalEngine)
        assert build.call_args.kwargs["collection"] == "documents"
        assert store.search.call_args.args[0] == "documents"
        assert store.search.call_args.kwargs["filters"] == {"tenant": "t1"}
        assert [r.document_id for r in results] == ["doc-1"]
