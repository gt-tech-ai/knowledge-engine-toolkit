"""Tests for the in-memory stub vector store + factory selection.

The ``stub`` vector store lets the retrieval path build and unit-test with no Qdrant — the
stub-first property (ARCHITECTURE.md#stub-first-backends) that makes the all-stubs build possible.
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.vector.builder import (
    VectorStoreConfig,
    VectorStoreKind,
    new_vector_store_from_config,
)
from techai_webutils.clients.vector.stub import StubVectorStore
from techai_webutils.core.interfaces.vector_store import VectorEntry


def _entry(
    entry_id: str, doc_id: str, content: str, embedding: list[float]
) -> VectorEntry:
    """Build a minimal ``VectorEntry`` for the stub-store tests."""
    return VectorEntry(
        id=entry_id,
        document_id=doc_id,
        content=content,
        embedding=embedding,
        metadata={},
    )


@pytest.mark.asyncio
async def test_vector_stub_upsert_search_roundtrip() -> None:
    """The stub store upserts entries and ranks search results by cosine similarity, deterministically.

    Why this test is important:
        - A stub vector store is only a useful no-infra stand-in if a query actually returns the
          nearest stored vector (so retrieval logic exercises a real ranking) and does so
          deterministically; an empty or random result would not exercise the path.

    What it tests:
        - After upserting two entries, a query aligned with the first returns it top-ranked;
          count/delete-by-document behave; the removed document no longer appears.
    """
    store = StubVectorStore(dimension=3)
    await store.upsert(
        "c",
        [
            _entry("1", "d1", "hello", [1.0, 0.0, 0.0]),
            _entry("2", "d2", "world", [0.0, 1.0, 0.0]),
        ],
    )

    results = await store.search("c", [1.0, 0.0, 0.0], top_k=1)
    assert [r.chunk_id for r in results] == ["1"], "the nearest vector ranks first"
    assert results[0].content == "hello"
    assert results[0].score == pytest.approx(1.0), (
        "an aligned query is a perfect cosine match"
    )

    assert await store.count_by_document("c", "d1") == 1

    await store.delete_by_document("c", "d1")
    remaining = await store.search("c", [1.0, 0.0, 0.0], top_k=10)
    assert [r.chunk_id for r in remaining] == ["2"], (
        "the deleted document no longer appears"
    )


def test_new_vector_store_from_config_selects_stub() -> None:
    """The factory returns the stub store for ``VectorStoreKind.STUB`` (config-selects-impl).

    Why this test is important:
        - The stub is opted into by a config kind; if the factory silently built the Qdrant client,
          the configured no-infra behavior would never take effect (and would dial Qdrant).

    What it tests:
        - ``new_vector_store_from_config`` with ``kind=STUB`` returns a ``StubVectorStore`` of the
          configured dimension.
    """
    store = new_vector_store_from_config(
        VectorStoreConfig(kind=VectorStoreKind.STUB, dimension=3)
    )
    assert isinstance(store, StubVectorStore)
    assert store.dimension == 3


@pytest.mark.asyncio
async def test_stub_delete_by_id_filtered_search_and_zero_vector() -> None:
    """Delete-by-id, metadata-filtered search, and a zero-norm query all behave.

    Why this test is important:
        - Metadata filtering scopes a query to a tenant/language; delete-by-id is how
          a single chunk is evicted; and a zero query vector must score 0 (not crash on
          a divide-by-zero) — all three are exercised by the retrieval path.

    What it tests:
        - A filter returns only matching-metadata entries; a zero query gives score 0
          for every entry; delete(ids) removes exactly the named entry.
    """
    store = StubVectorStore(dimension=3)
    await store.upsert(
        "c",
        [
            VectorEntry(
                id="1",
                document_id="d1",
                content="a",
                embedding=[1.0, 0.0, 0.0],
                metadata={"lang": "en"},
            ),
            VectorEntry(
                id="2",
                document_id="d2",
                content="b",
                embedding=[0.0, 1.0, 0.0],
                metadata={"lang": "fr"},
            ),
        ],
    )

    filtered = await store.search("c", [1.0, 1.0, 0.0], top_k=10, filters={"lang": "en"})
    assert [r.chunk_id for r in filtered] == ["1"], (
        "only the matching-metadata entry is returned"
    )

    zero = await store.search("c", [0.0, 0.0, 0.0], top_k=10)
    assert all(r.score == pytest.approx(0.0) for r in zero), (
        "a zero-norm query scores 0, not a crash"
    )

    await store.delete("c", ["1"])
    remaining = await store.search("c", [1.0, 0.0, 0.0], top_k=10)
    assert [r.chunk_id for r in remaining] == ["2"], "the deleted id is gone"


@pytest.mark.asyncio
async def test_stub_stored_metadata_reports_present_ids() -> None:
    """The stub answers the stored-metadata probe from its map, so a redrive can resume.

    Why this test is important:
        - The incremental indexer skips sub-batches whose ids are already stored with the same
          metadata; a stub that always reported "nothing stored" would hide that resume path
          from every no-infra test.

    What it tests:
        - ``stored_metadata`` returns the upserted metadata for each present id, omits absent ids
          and unknown collections, and ``existing_ids`` reports the same present subset.
    """
    store = StubVectorStore(dimension=3)
    await store.upsert(
        "c",
        [
            VectorEntry(
                id="1",
                document_id="d1",
                content="a",
                embedding=[1.0, 0.0, 0.0],
                metadata={"lang": "en"},
            )
        ],
    )

    assert await store.stored_metadata("c", ["1", "2"]) == {"1": {"lang": "en"}}
    assert await store.existing_ids("c", ["1", "2"]) == {"1"}
    assert await store.stored_metadata("missing", ["1"]) == {}
