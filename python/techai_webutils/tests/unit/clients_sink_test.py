"""Unit tests for the sinks and their factory (the vector store is mocked)."""

from __future__ import annotations

from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.sink import SinkConfig, SinkKind, sink_from_config
from techai_webutils.clients.sink.noop.sink import NoopSink
from techai_webutils.clients.sink.vector.sink import VectorStoreSink
from techai_webutils.clients.vector.opensearch import OpenSearchVectorStore
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.vector_store import VectorEntry

_DOC = SinkDocument(
    org_id="o1",
    document_id="d1",
    content_hash="h1",
    embedding_model="m",
    embedding_version="2",
    metadata={"clearance": "public"},
)


def _store(
    index: str | None = "idx", model: tuple[str, str] = ("m", "2")
) -> tuple[MagicMock, MagicMock]:
    """Return a mocked store and the org-scoped view its ``for_org`` hands out."""
    scoped = MagicMock(spec=OpenSearchVectorStore)
    scoped.current_index = AsyncMock(return_value=index)
    scoped.index_model = AsyncMock(return_value=model)
    scoped.configured_model = model
    scoped.upsert = AsyncMock()
    scoped.delete_by_document = AsyncMock()
    scoped.update_metadata = AsyncMock()
    store = MagicMock(spec=OpenSearchVectorStore)
    store.for_org = MagicMock(return_value=scoped)
    return store, scoped


def _sink(store: MagicMock) -> VectorStoreSink:
    """Build the opensearch sink through the factory over ``store``."""
    sink = sink_from_config(
        SinkConfig(kind=SinkKind.OPENSEARCH, collection="docs"), store=store
    )
    assert isinstance(sink, VectorStoreSink)
    return sink


@pytest.mark.asyncio
async def test_write_upserts_stable_ids_with_entities_and_content_hash() -> None:
    """Test that a write stores one entry per chunk with a stable id and every stored field.

    **Why this test is important:**
      - Stable ids make a rewrite replace a document's chunks instead of duplicating them;
        ``content_hash`` and the entity fields are what dedup and entity filters read.

    **What it tests:**
      - the store is scoped to the document's org; entries ``d1:0`` and ``d1:1`` carry the
        document metadata, ``org_id``, ``content_hash``, the model, the chunk's structure fields
        and ``entities.person`` joined with ``|``
    """
    store, scoped = _store()
    chunks = [
        ChunkPayload(
            text="a", section_path="Intro", entities={"person": ("Ada", "Grace")}
        ),
        ChunkPayload(text="b", language="en", metadata={"page": "2"}),
    ]

    await _sink(store).write(_DOC, chunks, [[1.0, 0.0], [0.0, 1.0]])

    store.for_org.assert_called_once_with("o1")
    collection, entries = scoped.upsert.await_args.args
    assert collection == "docs"
    assert [e.id for e in entries] == ["d1:0", "d1:1"]
    first: VectorEntry = entries[0]
    assert first.metadata == {
        "clearance": "public",
        "org_id": "o1",
        "content_hash": "h1",
        "embedding_model": "m",
        "embedding_version": "2",
        "chunk_index": "0",
        "section_path": "Intro",
        "entities.person": "Ada|Grace",
    }
    assert entries[1].metadata["language"] == "en"
    assert entries[1].metadata["page"] == "2"
    assert entries[1].embedding == [0.0, 1.0]


@pytest.mark.asyncio
@pytest.mark.parametrize("vectors", [None, [[1.0]]])
async def test_write_refuses_missing_or_mismatched_vectors(
    vectors: list[list[float]] | None,
) -> None:
    """Test that a write whose vectors do not pair one-to-one with its chunks is refused.

    **Why this test is important:**
      - A short vector list would silently drop chunks; a vector sink cannot embed by itself.

    **What it tests:**
      - no vectors, or one vector for two chunks, raises ``AppValueError``; nothing is written
    """
    store, scoped = _store()

    with pytest.raises(AppValueError):
        await _sink(store).write(
            _DOC, [ChunkPayload(text="a"), ChunkPayload(text="b")], vectors
        )

    scoped.upsert.assert_not_awaited()


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("index", "model"),
    [("idx", ("other", "2")), ("idx", ("m", "1")), (None, ("other", "2"))],
)
async def test_write_refuses_vectors_from_another_model(
    index: str | None, model: tuple[str, str]
) -> None:
    """Test that vectors from a model other than the index's are a permanent ``CONFLICT``.

    **Why this test is important:**
      - Two models' vectors in one index have equal dimensions but incomparable geometry; the
        index's search would degrade silently.

    **What it tests:**
      - an index built for another model or version, or a new index the store would stamp with
        another model, raises a permanent ``CONFLICT`` and writes nothing
    """
    store, scoped = _store(index=index, model=model)

    with pytest.raises(AppError) as info:
        await _sink(store).write(_DOC, [ChunkPayload(text="a")], [[1.0]])

    assert info.value.code is ErrorCode.CONFLICT
    assert info.value.is_permanent
    scoped.upsert.assert_not_awaited()


@pytest.mark.asyncio
async def test_delete_and_update_metadata_are_scoped_to_the_org() -> None:
    """Test that delete and a metadata update go to the document's org view of the collection.

    **Why this test is important:**
      - A clearance change must reach every chunk without a re-embed, and never another org's.

    **What it tests:**
      - ``delete`` and ``update_metadata`` call the ``o2`` view with the collection and document
    """
    store, scoped = _store()
    sink = _sink(store)

    await sink.delete("o2", "d9")
    await sink.update_metadata("o2", "d9", {"clearance": "secret"})

    assert [c.args for c in store.for_org.call_args_list] == [("o2",), ("o2",)]
    scoped.delete_by_document.assert_awaited_once_with("docs", "d9")
    scoped.update_metadata.assert_awaited_once_with("docs", "d9", {"clearance": "secret"})


@pytest.mark.asyncio
async def test_default_is_noop_and_factory_fails_loudly() -> None:
    """Test that the default sink stores nothing and a bad config fails at build time.

    **Why this test is important:**
      - The default keeps today's behaviour; a misconfigured sink must stop the app at boot.

    **What it tests:**
      - ``SinkConfig()`` builds ``NoopSink`` whose calls succeed; ``opensearch`` without a store
        and an unknown kind raise ``AppValueError``
    """
    sink = sink_from_config(SinkConfig())
    assert isinstance(sink, NoopSink)
    await sink.write(_DOC, [], None)
    await sink.delete("o", "d")
    await sink.update_metadata("o", "d", {})
    with pytest.raises(AppValueError):
        sink_from_config(SinkConfig(kind=SinkKind.OPENSEARCH))
    bogus = MagicMock(spec=SinkConfig)
    bogus.kind = "bogus"
    with pytest.raises(AppValueError):
        sink_from_config(bogus)
