"""Tests for the Qdrant vector store + factory (mocked qdrant-client — logic, not network)."""

from __future__ import annotations

import asyncio
import uuid
from unittest.mock import AsyncMock, MagicMock

import pytest
from qdrant_client import AsyncQdrantClient

from techai_webutils.clients.vector.builder import (
    VectorStoreConfig,
    VectorStoreKind,
    new_vector_store_from_config,
)
from techai_webutils.clients.vector.qdrant import QdrantVectorStore
from techai_webutils.core.interfaces.vector_store import VectorEntry


def _mock_client(
    hits: list[object] | None = None,
    *,
    exists: bool = True,
    retrieved: list[object] | None = None,
) -> AsyncQdrantClient:
    """Return a mock AsyncQdrantClient: collection_exists/create/upsert/delete + query_points→.points + retrieve."""
    client = MagicMock(spec=AsyncQdrantClient)
    client.collection_exists = AsyncMock(return_value=exists)
    client.create_collection = AsyncMock()
    client.upsert = AsyncMock()
    client.delete = AsyncMock()
    count = MagicMock()
    count.count = 3
    client.count = AsyncMock(return_value=count)
    response = MagicMock()
    response.points = hits or []
    client.query_points = AsyncMock(return_value=response)
    client.retrieve = AsyncMock(return_value=retrieved or [])
    return client


def _retrieved_point(entry_id: str, **metadata: str) -> object:
    """Return a retrieved point whose ``id`` is the deterministic Qdrant point id of ``entry_id``.

    Its payload carries the store's own keys plus ``metadata``, as ``QdrantVectorStore.upsert`` writes it.
    """
    point = MagicMock()
    point.id = str(uuid.uuid5(uuid.NAMESPACE_URL, entry_id))
    point.payload = {
        "entry_id": entry_id,
        "document_id": "d1",
        "content": "text",
        **metadata,
    }
    return point


class TestQdrantVectorStore:
    """Tests for the Qdrant vector store."""

    @pytest.mark.asyncio
    async def test_upsert_creates_missing_collection_and_writes_payload(self) -> None:
        """Upsert ensures the collection (cosine+dim) then upserts points carrying the metadata payload.

        Why this test is important:
          - The whole retrieval path depends on the payload (tenant/document_name/…) being written;
            missing keys silently break filtering + citations downstream.

        What it tests:
          - A missing collection is created; the point payload includes document_id, content, and the entry
            metadata.
        """
        client = _mock_client(exists=False)
        store = QdrantVectorStore(client, dimension=3)

        await store.upsert(
            "docs",
            [
                VectorEntry(
                    id="p1",
                    document_id="d1",
                    content="chunk text",
                    embedding=[0.1, 0.2, 0.3],
                    metadata={"tenant": "t1", "document_name": "Doc"},
                ),
            ],
        )

        client.create_collection.assert_awaited_once()
        client.upsert.assert_awaited_once()
        points = client.upsert.call_args.kwargs["points"]
        # Qdrant point id is the deterministic UUID of the entry id; the raw id is kept in the payload.
        assert points[0].id == str(uuid.uuid5(uuid.NAMESPACE_URL, "p1"))
        assert points[0].payload["entry_id"] == "p1"
        assert points[0].payload["tenant"] == "t1"
        assert points[0].payload["document_name"] == "Doc"
        assert points[0].payload["document_id"] == "d1"
        assert points[0].payload["content"] == "chunk text"

    @pytest.mark.asyncio
    async def test_concurrent_first_upserts_create_collection_once(self) -> None:
        """Concurrent first-time upserts create the collection exactly once (no TOCTOU race).

        Why this test is important:
          - An indexer that fans out concurrent upserts would, without serializing the lazy create, have
            two coroutines both see the collection missing and both create it — the loser errors
            "already exists" and fails an otherwise-good document.

        What it tests:
          - Ten concurrent upserts to a missing collection await create_collection exactly once.
        """
        client = _mock_client(exists=False)
        store = QdrantVectorStore(client, dimension=3)
        entry = VectorEntry(
            id="p", document_id="d", content="c", embedding=[0.1, 0.2, 0.3], metadata={}
        )
        await asyncio.gather(*(store.upsert("docs", [entry]) for _ in range(10)))

        client.create_collection.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_concurrent_create_across_processes_tolerates_already_exists(
        self,
    ) -> None:
        """A cross-process create race (409 "already exists") is swallowed when the collection now exists.

        Why this test is important:
          - The per-instance asyncio lock only serializes ONE process; several indexing processes can
            still race create_collection on a first-time upsert. The loser gets a 409 and, without
            tolerating it, fails an otherwise-good document.

        What it tests:
          - create_collection raises (the 409); on re-check the collection exists → upsert completes with
            no error propagated.
        """
        client = _mock_client()
        # Missing on the pre-create guard, present on the post-failure re-check (a rival won the race).
        client.collection_exists = AsyncMock(side_effect=[False, True])
        client.create_collection = AsyncMock(
            side_effect=RuntimeError("409 (Conflict): already exists!")
        )
        store = QdrantVectorStore(client, dimension=3)
        entry = VectorEntry(
            id="p", document_id="d", content="c", embedding=[0.1, 0.2, 0.3], metadata={}
        )

        await store.upsert("docs", [entry])  # must not raise

        client.upsert.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_create_failure_propagates_when_collection_still_absent(self) -> None:
        """A genuine create failure (collection still absent on re-check) propagates — not silently eaten.

        Why this test is important:
          - Tolerating the 409 race must not mask a real create failure (bad dimension, unreachable
            Qdrant); the document should fail loudly so it can be retried/redriven, not silently skipped.

        What it tests:
          - create_collection raises and the collection still does not exist on re-check → the error
            propagates out of upsert.
        """
        client = _mock_client()
        client.collection_exists = AsyncMock(
            return_value=False
        )  # absent before AND after the failed create
        client.create_collection = AsyncMock(
            side_effect=RuntimeError("connection refused")
        )
        store = QdrantVectorStore(client, dimension=3)
        entry = VectorEntry(
            id="p", document_id="d", content="c", embedding=[0.1, 0.2, 0.3], metadata={}
        )

        with pytest.raises(RuntimeError, match="connection refused"):
            await store.upsert("docs", [entry])

    @pytest.mark.asyncio
    async def test_search_filters_by_scope_and_maps_payload(self) -> None:
        """Search applies a payload scope filter and maps each hit's payload into VectorSearchResult.metadata.

        Why this test is important:
          - Without the pushed-down scope filter, queries mix scopes (e.g. tenants) before the policy
            filter; without the payload→metadata mapping, FilteringRetrievalEngine drops every result.

        What it tests:
          - query_points is called with a non-None query_filter; the result carries document_id, the
            *semantic* chunk id (payload entry_id, not the opaque point UUID), content, score, and the
            payload metadata (tenant, document_name) with the promoted keys excluded.
        """
        hit = MagicMock()
        hit.id = str(
            uuid.uuid5(uuid.NAMESPACE_URL, "d1:3")
        )  # the opaque Qdrant point id (a UUID)
        hit.score = 0.91
        hit.payload = {
            "entry_id": "d1:3",  # the original semantic chunk id
            "document_id": "d1",
            "content": "the chunk",
            "tenant": "t1",
            "document_name": "Doc",
            "page_number": "4",
        }
        client = _mock_client([hit])
        store = QdrantVectorStore(client, dimension=3)

        results = await store.search(
            "docs", [0.1, 0.2, 0.3], top_k=5, filters={"tenant": "t1"}
        )

        client.query_points.assert_awaited_once()
        assert client.query_points.call_args.kwargs["query_filter"] is not None
        assert client.query_points.call_args.kwargs["limit"] == 5
        assert len(results) == 1
        r = results[0]
        assert r.document_id == "d1"
        # chunk_id is the semantic entry_id, NOT the opaque point UUID.
        assert r.chunk_id == "d1:3"
        assert r.content == "the chunk"
        assert r.score == pytest.approx(0.91)
        assert r.metadata["tenant"] == "t1"
        assert r.metadata["document_name"] == "Doc"
        assert r.metadata["page_number"] == "4"
        # Keys promoted to top-level fields are not duplicated in metadata.
        assert "content" not in r.metadata
        assert "document_id" not in r.metadata
        assert "entry_id" not in r.metadata

    @pytest.mark.asyncio
    async def test_delete_by_document_uses_a_filter_selector(self) -> None:
        """delete_by_document deletes by a document_id filter (not by point id)."""
        client = _mock_client()
        store = QdrantVectorStore(client, dimension=3)
        await store.delete_by_document("docs", "d1")
        client.delete.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_count_by_document_returns_zero_for_missing_collection(self) -> None:
        """count_by_document short-circuits to 0 when the collection does not exist (no count call)."""
        client = _mock_client(exists=False)
        store = QdrantVectorStore(client, dimension=3)
        assert await store.count_by_document("docs", "d1") == 0
        client.count.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_count_by_document_counts_when_collection_exists(self) -> None:
        """count_by_document returns the store's exact count for the document filter."""
        client = _mock_client(exists=True)
        store = QdrantVectorStore(client, dimension=3)
        assert await store.count_by_document("docs", "d1") == 3
        client.count.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_existing_ids_empty_for_missing_collection(self) -> None:
        """existing_ids short-circuits to an empty set when the collection does not exist (no retrieve)."""
        client = _mock_client(exists=False)
        store = QdrantVectorStore(client, dimension=3)
        assert await store.existing_ids("docs", ["d1:0", "d1:1"]) == set()
        client.retrieve.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_existing_ids_returns_the_present_subset(self) -> None:
        """existing_ids maps raw ids to point ids, retrieves, and reports which raw ids are present.

        Why this test is important:
          - The incremental indexer's resume skips a sub-batch only when EVERY id in it is already
            present. The raw-id → deterministic uuid5 point-id → raw-id round-trip is the load-bearing
            mapping; if it drifts, resume would re-embed done work or (worse) skip un-indexed chunks.

        What it tests:
          - With points for d1:0 and d1:2 present (d1:1 absent), existing_ids(["d1:0","d1:1","d1:2"])
            returns exactly {"d1:0","d1:2"}, and retrieve is called with the three deterministic point ids.
        """
        client = _mock_client(
            retrieved=[_retrieved_point("d1:0"), _retrieved_point("d1:2")]
        )
        store = QdrantVectorStore(client, dimension=3)

        present = await store.existing_ids("docs", ["d1:0", "d1:1", "d1:2"])

        assert present == {"d1:0", "d1:2"}
        retrieved_ids = client.retrieve.call_args.kwargs["ids"]
        assert set(retrieved_ids) == {
            str(uuid.uuid5(uuid.NAMESPACE_URL, raw)) for raw in ("d1:0", "d1:1", "d1:2")
        }

    @pytest.mark.asyncio
    async def test_stored_metadata_returns_each_present_points_metadata(self) -> None:
        """stored_metadata reports, per present raw id, the metadata stored with it (not the store's keys).

        Why this test is important:
          - The incremental indexer skips a sub-batch only when every chunk is present WITH the metadata
            it would write; returning the wrong payload slice would either skip a reclassified chunk
            (keeping its old scope) or re-embed everything on every redrive.

        What it tests:
          - With d1:0 stored as {tenant: t1, chunk_index: 0} and d1:1 absent, stored_metadata returns
            exactly {"d1:0": {"tenant": "t1", "chunk_index": "0"}}, fetched with the payload and without
            vectors; a missing collection returns {} without a retrieve.
        """
        client = _mock_client(
            retrieved=[_retrieved_point("d1:0", tenant="t1", chunk_index="0")]
        )
        store = QdrantVectorStore(client, dimension=3)

        stored = await store.stored_metadata("docs", ["d1:0", "d1:1"])

        assert stored == {"d1:0": {"tenant": "t1", "chunk_index": "0"}}
        assert client.retrieve.call_args.kwargs["with_payload"] is True
        assert client.retrieve.call_args.kwargs["with_vectors"] is False

        missing = _mock_client(exists=False)
        assert (
            await QdrantVectorStore(missing, dimension=3).stored_metadata(
                "docs", ["d1:0"]
            )
            == {}
        )
        missing.retrieve.assert_not_awaited()

    def test_dimension_exposed(self) -> None:
        """The store exposes its collection dimension (single source of truth vs the embedder)."""
        assert QdrantVectorStore(_mock_client(), dimension=768).dimension == 768


class TestVectorStoreFactory:
    """Tests for the vector store factory."""

    def test_from_config_builds_qdrant(self) -> None:
        """new_vector_store_from_config(kind=qdrant) builds a QdrantVectorStore."""
        store = new_vector_store_from_config(
            VectorStoreConfig(
                kind=VectorStoreKind.QDRANT,
                url="http://qdrant:6333",
                collection="docs",
                dimension=768,
            ),
        )
        assert isinstance(store, QdrantVectorStore)
        assert store.dimension == 768

    def test_unknown_kind_raises(self) -> None:
        """An unknown vector-store kind fails loudly."""
        with pytest.raises(ValueError, match="unknown vector store kind"):
            new_vector_store_from_config(VectorStoreConfig(kind="bogus"))  # type: ignore[arg-type]
