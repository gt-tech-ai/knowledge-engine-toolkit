"""Tests for the vector-backed dev KnowledgeBase (chunk indexer) + factory (mocked deps)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.vector_kb.builder import (
    KnowledgeBaseConfig,
    KnowledgeBaseKind,
    new_knowledge_base_from_config,
)
from techai_webutils.clients.vector_kb.vector import VectorKnowledgeBase
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.vector_store import VectorStore


def _embedder(dimension: int = 3) -> EmbeddingProvider:
    """Mock EmbeddingProvider: embed_batch returns one EmbeddingResult per input chunk."""
    embedder = MagicMock(spec=EmbeddingProvider)
    embedder.dimension.return_value = dimension
    embedder.embed_batch = AsyncMock(
        side_effect=lambda texts: [
            EmbeddingResult(embedding=[0.1, 0.2, 0.3], model="m", token_count=0)
            for _ in texts
        ],
    )
    return embedder


def _store(
    count: int = 0, dimension: int = 3, stored: dict[str, dict[str, str]] | None = None
) -> VectorStore:
    """Mock VectorStore: upsert/delete_by_document + count_by_document returning `count`.

    ``stored_metadata`` reports the checkpoint map (``stored``: chunk id -> stored metadata, default
    empty) restricted to the queried ids, so a test can pre-declare which chunk ids are already indexed,
    and with which metadata, to drive the resume path.
    """
    already = stored or {}
    store = MagicMock(spec=VectorStore)
    store.dimension = dimension
    store.upsert = AsyncMock()
    store.delete_by_document = AsyncMock()
    store.count_by_document = AsyncMock(return_value=count)
    store.stored_metadata = AsyncMock(
        side_effect=lambda _collection, ids: {i: already[i] for i in ids if i in already}
    )
    return store


def _indexed(
    document_id: str, indexes: range, **attributes: str
) -> dict[str, dict[str, str]]:
    """Return the stored metadata of chunks ``indexes`` of a document indexed with ``attributes`` and no name."""
    return {
        f"{document_id}:{i}": {
            **attributes,
            "document_name": document_id,
            "chunk_index": str(i),
        }
        for i in indexes
    }


class TestVectorKnowledgeBase:
    """Tests for the vector knowledge base."""

    @pytest.mark.asyncio
    async def test_index_document_embeds_and_upserts_with_metadata_schema(self) -> None:
        """index_document embeds each chunk and upserts points carrying the consumer's attributes.

        Why this test is important:
          - Retrieval pushes down and re-validates the consumer's scope keys; if index_document dropped
            the attributes, the passages would be indexed but every query would filter them out.

        What it tests:
          - embed_batch is called with the chunks; upsert writes one VectorEntry per chunk with the
            given attributes (tenant, level), the document_id and the chunk_index.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, collection="documents")

        await kb.index_document(
            "doc-1",
            ["first chunk", "second chunk"],
            attributes={"tenant": "t1", "level": "mid"},
        )

        embedder.embed_batch.assert_awaited_once_with(["first chunk", "second chunk"])
        store.upsert.assert_awaited_once()
        entries = store.upsert.call_args.args[1]
        assert len(entries) == 2
        assert entries[0].id == "doc-1:0"
        assert entries[0].document_id == "doc-1"
        assert entries[0].content == "first chunk"
        assert entries[0].metadata["tenant"] == "t1"
        assert entries[0].metadata["level"] == "mid"
        assert entries[0].metadata["chunk_index"] == "0"
        # No document_name passed → falls back to the document_id.
        assert entries[0].metadata["document_name"] == "doc-1"

    @pytest.mark.asyncio
    async def test_index_document_stamps_document_name_for_citation(self) -> None:
        """A passed document_name is stamped onto every chunk so a citation can display the filename.

        Why this test is important:
          - The local-vector path has no native citations; the retrieval side reads ``document_name``
            from each chunk's metadata to show a human-readable source (e.g. ``moby-dick.txt``). Without
            threading the uploaded filename through, every citation shows the opaque document id.

        What it tests:
          - index_document(document_name="moby-dick.txt") writes that name onto each upserted chunk's
            metadata (not the document_id).
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")

        await kb.index_document("doc-1", ["a chunk"], document_name="moby-dick.txt")

        entries = store.upsert.call_args.args[1]
        assert entries[0].metadata["document_name"] == "moby-dick.txt"

    @pytest.mark.asyncio
    async def test_index_document_empty_document_name_falls_back_to_id(self) -> None:
        """An EXPLICIT empty document_name (e.g. a source with no file name) falls back to the id.

        Why this test is important:
          - A caller often threads an optional source name straight through, passing "" when it has
            none. This pins the ``document_name or document_id`` fallback for the empty-string case (not
            just an omitted arg), so a citation never renders a blank source label.

        What it tests:
          - index_document(document_name="") stamps the document_id onto each chunk's ``document_name``.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")

        await kb.index_document("doc-1", ["a chunk"], document_name="")

        entries = store.upsert.call_args.args[1]
        assert entries[0].metadata["document_name"] == "doc-1"

    @pytest.mark.asyncio
    async def test_index_document_empty_chunks_is_a_noop(self) -> None:
        """Indexing zero chunks embeds nothing and upserts nothing.

        Why this test is important:
          - An empty parse must not call the embedder with an empty batch or write an empty upsert.

        What it tests:
          - index_document("doc-1", []) awaits neither embed_batch nor upsert.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")
        await kb.index_document("doc-1", [])
        embedder.embed_batch.assert_not_awaited()
        store.upsert.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_index_document_upserts_incrementally_per_sub_batch(self) -> None:
        """A multi-sub-batch document embeds AND upserts one sub-batch at a time, not all-at-once.

        Why this test is important:
          - A large document (thousands of chunks) must not embed everything into memory and then do a
            single all-or-nothing upsert: on the CPU-Ollama path that one giant unit times out and the
            whole document is lost. Incremental per-sub-batch upsert bounds peak memory and — because
            each sub-batch is persisted as it completes — lets a redrive resume instead of restarting.

        What it tests:
          - 150 chunks (sub-batch size 96) drive TWO embed_batch calls and TWO upserts (96 then 54),
            and the emitted point ids span the whole document (doc-1:0 .. doc-1:149) with no gaps.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")
        chunks = [f"chunk-{i}" for i in range(150)]

        await kb.index_document("doc-1", chunks)

        assert embedder.embed_batch.await_count == 2
        assert store.upsert.await_count == 2
        upserted_ids = [
            e.id for call in store.upsert.call_args_list for e in call.args[1]
        ]
        assert upserted_ids == [f"doc-1:{i}" for i in range(150)]

    @pytest.mark.asyncio
    async def test_index_document_skips_already_indexed_sub_batches(self) -> None:
        """A redrive re-embeds ONLY the chunks not already in the store (checkpoint resume).

        Why this test is important:
          - Embedding is the expensive CPU step. When an at-least-once redrive (or a mid-ingest worker
            restart) re-runs a partially indexed document, re-embedding the sub-batches already upserted
            is wasted CPU that can keep a large document from ever converging. The stable point ids make
            a sub-batch skippable when every id in it is already present.

        What it tests:
          - With the first sub-batch (doc-1:0..95) already stored with the same metadata, a 150-chunk
            re-index embeds and upserts ONLY the second sub-batch (the 54 remaining chunks), leaving the
            done work untouched.
        """
        embedder = _embedder()
        store = _store(stored=_indexed("doc-1", range(96)))
        kb = VectorKnowledgeBase(embedder, store, "documents")
        chunks = [f"chunk-{i}" for i in range(150)]

        await kb.index_document("doc-1", chunks)

        embedder.embed_batch.assert_awaited_once_with(chunks[96:])
        store.upsert.assert_awaited_once()
        upserted_ids = [e.id for e in store.upsert.call_args.args[1]]
        assert upserted_ids == [f"doc-1:{i}" for i in range(96, 150)]

    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("stored", "reindex"),
        [
            (
                _indexed("doc-1", range(150), level="public"),
                {"attributes": {"level": "restricted"}},
            ),
            (
                _indexed("doc-1", range(150), level="public", tenant="t1"),
                {"attributes": {"level": "public"}},
            ),
            (
                _indexed("doc-1", range(150), level="public"),
                {"attributes": {"level": "public"}, "document_name": "a.pdf"},
            ),
        ],
        ids=["changed-attribute", "removed-attribute", "changed-document-name"],
    )
    async def test_index_document_rewrites_sub_batches_whose_stored_metadata_differs(
        self, stored: dict[str, dict[str, str]], reindex: dict[str, object]
    ) -> None:
        """Re-indexing a present document with other attributes or name rewrites every chunk's metadata.

        Why this test is important:
          - Attributes carry the consumer's scope (tenant, sensitivity label). If resume skipped chunks
            that are present but stamped with the old scope, a reclassified document would keep serving
            its old label — e.g. restricted content still admitted to public callers.

        What it tests:
          - With all 150 chunks stored under different metadata (a changed attribute, an attribute the
            new call no longer sets, or another document name), a re-index embeds and upserts both
            sub-batches, and the upserted entries carry exactly the new metadata.
        """
        embedder, store = _embedder(), _store(stored=stored)
        kb = VectorKnowledgeBase(embedder, store, "documents")

        await kb.index_document("doc-1", [f"chunk-{i}" for i in range(150)], **reindex)  # type: ignore[arg-type]

        assert embedder.embed_batch.await_count == 2
        entries = [e for call in store.upsert.call_args_list for e in call.args[1]]
        assert [e.id for e in entries] == [f"doc-1:{i}" for i in range(150)]
        expected_name = str(reindex.get("document_name", "doc-1"))
        assert entries[5].metadata == {
            **reindex["attributes"],  # type: ignore[dict-item]
            "document_name": expected_name,
            "chunk_index": "5",
        }

    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        "key", ["document_id", "content", "entry_id", "document_name", "chunk_index"]
    )
    async def test_index_document_rejects_attributes_that_shadow_reserved_keys(
        self, key: str
    ) -> None:
        """An attribute named like a key the index writes itself is refused before anything is embedded.

        Why this test is important:
          - Attributes are spread into the stored payload; one named ``document_id`` would overwrite the
            payload id, so remove_document/get_document_status would match nothing and orphan the
            points, and one named ``content`` would replace the chunk text.

        What it tests:
          - index_document(attributes={<reserved>: "x"}) raises ValueError naming the key and neither
            embeds nor upserts.
        """
        embedder, store = _embedder(), _store()
        kb = VectorKnowledgeBase(embedder, store, "documents")

        with pytest.raises(ValueError, match=key):
            await kb.index_document("doc-1", ["a chunk"], attributes={key: "x"})

        embedder.embed_batch.assert_not_awaited()
        store.upsert.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_get_document_status_ready_when_points_exist_else_none(self) -> None:
        """get_document_status reports ready+chunk_count when points exist, None when the doc is absent.

        Why this test is important:
          - The ingest/status API surfaces this; a wrong "ready" on an empty doc, or None on an indexed
            doc, misreports ingestion state to the UI.

        What it tests:
          - count>0 -> KBDocument(status="ready", chunk_count=count); count==0 -> None.
        """
        embedder = _embedder()
        ready = VectorKnowledgeBase(embedder, _store(count=5), "documents")
        doc = await ready.get_document_status("doc-1")
        assert doc is not None
        assert doc.status == "ready"
        assert doc.chunk_count == 5

        missing = VectorKnowledgeBase(embedder, _store(count=0), "documents")
        assert await missing.get_document_status("absent") is None

    @pytest.mark.asyncio
    async def test_remove_document_deletes_by_document(self) -> None:
        """remove_document deletes every chunk of the document from the store.

        Why this test is important:
          - A removed document must stop being retrievable; deleting by id list would miss chunks the
            caller no longer knows about.

        What it tests:
          - remove_document("doc-1") awaits delete_by_document on the KB's collection with "doc-1".
        """
        store = _store()
        kb = VectorKnowledgeBase(_embedder(), store, "documents")
        await kb.remove_document("doc-1")
        store.delete_by_document.assert_awaited_once_with("documents", "doc-1")

    def test_dimension_mismatch_fails_loudly(self) -> None:
        """A drifted embedder/store dimension raises at construction (single source of truth)."""
        with pytest.raises(ValueError, match="dimension"):
            VectorKnowledgeBase(
                _embedder(dimension=768), _store(dimension=3), "documents"
            )


class TestKnowledgeBaseFactory:
    """Tests for the knowledge base factory."""

    def test_from_config_builds_vector_kb(self) -> None:
        """new_knowledge_base_from_config(kind=vector) builds a VectorKnowledgeBase."""
        kb = new_knowledge_base_from_config(
            KnowledgeBaseConfig(
                kind=KnowledgeBaseKind.VECTOR,
                vector_url="http://qdrant:6333",
                collection="documents",
                embedding_host="http://ollama:11434",
                embedding_dimension=768,
            ),
        )
        assert isinstance(kb, VectorKnowledgeBase)

    def test_unknown_kind_raises(self) -> None:
        """An unknown knowledge-base kind fails loudly."""
        with pytest.raises(ValueError, match="unknown knowledge base kind"):
            new_knowledge_base_from_config(KnowledgeBaseConfig(kind="bogus"))  # type: ignore[arg-type]
