"""Unit tests for the sinks and their factory (the vector store is mocked)."""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.sink import SinkConfig, SinkKind, sink_from_config
from techai_webutils.clients.sink.noop.sink import NoopSink
from techai_webutils.clients.sink.vector.sink import VectorStoreSink
from techai_webutils.clients.vector.opensearch import OpenSearchVectorStore
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument
from tests.json_path import dig

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
    """Return a mocked store and the org-scoped view its ``for_org`` hands out.

    Returns:
        A mocked store and the org-scoped view its ``for_org`` hands out.

    """
    scoped = MagicMock(spec=OpenSearchVectorStore)
    scoped.current_index = AsyncMock(return_value=index)
    scoped.index_model = AsyncMock(return_value=model)
    scoped.configured_model = model
    scoped.upsert = AsyncMock()
    scoped.delete_by_document = AsyncMock()
    scoped.update_metadata = AsyncMock()
    scoped.stored_metadata = AsyncMock(return_value={})
    scoped.count_by_document = AsyncMock(return_value=0)
    store = MagicMock(spec=OpenSearchVectorStore)
    store.for_org = MagicMock(return_value=scoped)
    return store, scoped


def _sink(store: MagicMock) -> VectorStoreSink:
    """Build the opensearch sink through the factory over ``store``.

    Returns:
        The opensearch sink through the factory over ``store``.

    """
    sink = sink_from_config(
        SinkConfig(kind=SinkKind.OPENSEARCH, collection="docs"), store=store
    )
    assert isinstance(sink, VectorStoreSink)
    return sink


@pytest.mark.asyncio
async def test_write_upserts_stable_ids_with_entities_and_content_hash() -> None:
    """Test that a write stores one entry per chunk with a stable id.

    And every stored field.

    **Why this test is important:**
      - Stable ids make a rewrite replace a document's chunks instead of duplicating them;
        ``content_hash`` and the entity fields are what dedup and entity filters read.

    **What it tests:**
      - the store is scoped to the document's org; entries ``d1:0`` and ``d1:1`` carry the
        document metadata, ``org_id``, ``content_hash``, the model, the chunk's structure
        fields and ``entities.person`` joined with ``|``
    """
    store, scoped = _store()
    chunks = [
        ChunkPayload(
            text="a", section_path="Intro", entities={"person": ("Ada", "Grace")}
        ),
        ChunkPayload(text="b", language="en", metadata={"page": "2"}),
    ]

    await _sink(store).write(_DOC, chunks, [[1.0, 0.0], [0.0, 1.0]])

    cast("MagicMock", cast("MagicMock", store.for_org).assert_called_once_with)("o1")
    collection, entries = cast(
        "tuple[object, list[VectorEntry]]",
        cast("MagicMock", cast("AsyncMock", scoped.upsert).await_args).args,
    )
    assert collection == "docs"
    assert [e.id for e in entries] == [
        "d1:0",
        "d1:1",
    ]
    first = entries[0]
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
    assert cast("dict[str, object]", dig(entries, 1).metadata)["language"] == "en"
    assert cast("dict[str, object]", dig(entries, 1).metadata)["page"] == "2"
    assert cast("object", dig(entries, 1).embedding) == [0.0, 1.0]


@pytest.mark.asyncio
@pytest.mark.parametrize("vectors", [None, [[1.0]]])
async def test_write_refuses_missing_or_mismatched_vectors(
    vectors: list[list[float]] | None,
) -> None:
    """Test that a write whose vectors do not pair one-to-one with its chunks is refused.

    **Why this test is important:**
      - A short vector list would silently drop chunks; a vector sink cannot embed by
        itself.

    **What it tests:**
      - no vectors, or one vector for two chunks, raises ``AppValueError``; nothing is
        written
    """
    store, scoped = _store()

    with pytest.raises(AppValueError):
        await _sink(store).write(
            _DOC, [ChunkPayload(text="a"), ChunkPayload(text="b")], vectors
        )

    cast("MagicMock", cast("AsyncMock", scoped.upsert).assert_not_awaited)()


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
      - Two models' vectors in one index have equal dimensions but incomparable geometry;
        the index's search would degrade silently.

    **What it tests:**
      - an index built for another model or version, or a new index the store would stamp
        with another model, raises a permanent ``CONFLICT`` and writes nothing
    """
    store, scoped = _store(index=index, model=model)

    with pytest.raises(AppError) as info:
        await _sink(store).write(_DOC, [ChunkPayload(text="a")], [[1.0]])

    assert info.value.code is ErrorCode.CONFLICT
    assert info.value.is_permanent
    cast("MagicMock", cast("AsyncMock", scoped.upsert).assert_not_awaited)()


@pytest.mark.asyncio
async def test_redelivered_write_sends_only_chunks_not_stored_as_is() -> None:
    """Test that a resumed write skips the chunks already stored with the same metadata.

    **Why this test is important:**
      - A redelivery after a partial write re-sends the whole document unless the stored
        chunks are skipped; a chunk stored under another version or model must still be
        rewritten.

    **What it tests:**
      - with ``d1:0`` stored exactly as it would be written and ``d1:1`` stored with
        another ``content_hash``, only ``d1:1`` is upserted; nothing is deleted
    """
    store, scoped = _store()
    sink = _sink(store)
    await sink.write(
        _DOC, [ChunkPayload(text="a"), ChunkPayload(text="b")], [[1.0], [2.0]]
    )
    written = cast(
        "list[object]",
        cast("MagicMock", cast("AsyncMock", scoped.upsert).await_args).args[1],
    )
    stale = cast("object", written[1].metadata | {"content_hash": "old"})
    cast("MagicMock", scoped.stored_metadata).return_value = {
        "d1:0": cast("object", written[0].metadata),
        "d1:1": stale,
    }
    cast("MagicMock", scoped.count_by_document).return_value = 2
    cast("MagicMock", cast("MagicMock", scoped.upsert).reset_mock)()

    await sink.write(
        _DOC, [ChunkPayload(text="a"), ChunkPayload(text="b")], [[1.0], [2.0]]
    )

    assert [
        cast("object", e.id)
        for e in cast(
            "list[object]",
            cast("MagicMock", cast("AsyncMock", scoped.upsert).await_args).args[1],
        )
    ] == ["d1:1"]
    cast("MagicMock", cast("AsyncMock", scoped.stored_metadata).assert_awaited_with)(
        "docs", ["d1:0", "d1:1"]
    )
    cast("MagicMock", cast("AsyncMock", scoped.delete_by_document).assert_not_awaited)()


@pytest.mark.asyncio
async def test_write_of_a_shorter_version_deletes_the_extra_chunks() -> None:
    """Test that a document holding more chunks than the write loses the extras.

    **Why this test is important:**
      - A re-uploaded document with fewer chunks would otherwise keep the old version's
        tail searchable, citing text the document no longer has.

    **What it tests:**
      - the store holds 3 entries of which the write's one id is stored; the write deletes
        the document's entries except ``d1:0``
    """
    store, scoped = _store()
    cast("MagicMock", scoped.stored_metadata).return_value = {
        "d1:0": {"content_hash": "old"}
    }
    cast("MagicMock", scoped.count_by_document).return_value = 3

    await _sink(store).write(_DOC, [ChunkPayload(text="a")], [[1.0]])

    cast(
        "MagicMock", cast("AsyncMock", scoped.delete_by_document).assert_awaited_once_with
    )("docs", "d1", ["d1:0"])


@pytest.mark.asyncio
async def test_chunk_ordinal_sets_its_id() -> None:
    """Test that a chunk's ordinal, when set, is its id and ``chunk_index``.

    **Why this test is important:**
      - A caller writes only the chunks it embedded; numbering them by position would
        shift every later chunk's id and break citations against the whole document.

    **What it tests:**
      - chunks with ordinals 0 and 2 are written as ``d1:0`` and ``d1:2``, ``chunk_index``
        "2"
    """
    store, scoped = _store()
    chunks = [ChunkPayload(text="a", ordinal=0), ChunkPayload(text="c", ordinal=2)]

    await _sink(store).write(_DOC, chunks, [[1.0], [2.0]])

    entries = cast(
        "list[object]",
        cast("MagicMock", cast("AsyncMock", scoped.upsert).await_args).args[1],
    )
    assert [cast("object", e.id) for e in entries] == [
        "d1:0",
        "d1:2",
    ]
    assert cast("dict[str, object]", entries[1].metadata)["chunk_index"] == "2"


@pytest.mark.asyncio
async def test_delete_and_update_metadata_are_scoped_to_the_org() -> None:
    """Test that delete and a metadata update go to the document's org view.

    Of the collection.

    **Why this test is important:**
      - A clearance change must reach every chunk without a re-embed, and never another
        org's.

    **What it tests:**
      - ``delete`` and ``update_metadata`` call the ``o2`` view with the collection and
        document
    """
    store, scoped = _store()
    sink = _sink(store)

    await sink.delete("o2", "d9")
    await sink.update_metadata("o2", "d9", {"clearance": "secret"})

    assert [
        cast("object", c.args)
        for c in cast("list[object]", cast("MagicMock", store.for_org).call_args_list)
    ] == [("o2",), ("o2",)]
    cast(
        "MagicMock", cast("AsyncMock", scoped.delete_by_document).assert_awaited_once_with
    )("docs", "d9")
    cast("MagicMock", cast("AsyncMock", scoped.update_metadata).assert_awaited_once_with)(
        "docs", "d9", {"clearance": "secret"}
    )


@pytest.mark.asyncio
async def test_default_is_noop_and_factory_fails_loudly() -> None:
    """Test that the default sink stores nothing and a bad config fails at build time.

    **Why this test is important:**
      - The default keeps today's behaviour; a misconfigured sink must stop the app at
        boot.

    **What it tests:**
      - ``SinkConfig()`` builds ``NoopSink`` whose calls succeed; ``opensearch`` without a
        store and an unknown kind raise ``AppValueError``
    """
    sink = sink_from_config(SinkConfig())
    assert isinstance(sink, NoopSink)
    await sink.write(_DOC, [], None)
    await sink.delete("o", "d")
    await sink.update_metadata("o", "d", {})
    with pytest.raises(AppValueError):
        _ = sink_from_config(SinkConfig(kind=SinkKind.OPENSEARCH))
    bogus = MagicMock(spec=SinkConfig)
    bogus.kind = "bogus"
    with pytest.raises(AppValueError):
        _ = sink_from_config(bogus)
