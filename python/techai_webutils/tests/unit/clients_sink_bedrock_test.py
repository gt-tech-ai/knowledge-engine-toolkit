"""Unit tests for the Bedrock sink (mocked object store)."""

from __future__ import annotations

import hashlib
import json
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.sink import (
    BedrockLayout,
    BedrockSinkConfig,
    SinkConfig,
    SinkKind,
    sink_from_config,
)
from techai_webutils.clients.sink.bedrock import unit_prefix
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.sink import ChunkPayload, Sink, SinkDocument
from techai_webutils.core.interfaces.storage import StorageClient, StorageObject
from tests.json_path import dig

if TYPE_CHECKING:
    from collections.abc import Mapping

_EXPECTED_AWAIT_ARGS_LIST_COUNT = 6
_PAGELESS_INDEX = 2

_HASH2 = hashlib.sha256(b"doc1").hexdigest()[:2]
_PREFIX = f"kb/o1/{_HASH2}/doc1/"
_VERSION = "c" * 16
_DOC = SinkDocument(
    org_id="o1",
    document_id="doc1",
    content_hash="c" * 64,
    embedding_model="",
    embedding_version="",
    metadata={"classification": "internal"},
)
_CHUNKS = [
    ChunkPayload("alpha", section_path="Intro", entities={"ORG": ("Acme", "Initech")}),
    ChunkPayload("beta"),
    ChunkPayload("gamma", language="de"),
]


def _storage(existing: list[str] | None = None) -> MagicMock:
    """Return a mocked object store listing ``existing`` under any prefix.

    Args:
        existing: The keys already stored.

    Returns:
        A mocked object store listing ``existing`` under any prefix.

    """
    storage = MagicMock(spec=StorageClient)
    storage.list_objects = AsyncMock(
        return_value=[StorageObject(k, 1, "", "", "") for k in existing or []]
    )
    storage.upload = AsyncMock()
    storage.delete = AsyncMock()
    storage.download = AsyncMock()
    return storage


def _sink(
    storage: MagicMock,
    layout: BedrockLayout = BedrockLayout.CHUNK_FILES,
    object_tags: Mapping[str, str] | None = None,
) -> Sink:
    """Return the factory's bedrock sink over ``storage`` in bucket ``kb-bucket``.

    Args:
        storage: The mocked storage client.
        layout: How documents are laid out.
        object_tags: The tags the sink puts on every object it writes.

    Returns:
        The factory's bedrock sink over ``storage`` in bucket ``kb-bucket``.

    """
    return sink_from_config(
        SinkConfig(
            kind=SinkKind.BEDROCK,
            bedrock=BedrockSinkConfig(
                bucket="kb-bucket",
                layout=layout,
                max_unit_bytes=12,
                object_tags=object_tags or {},
            ),
        ),
        storage=storage,
    )


def _uploads(storage: MagicMock) -> dict[str, bytes]:
    """Return each uploaded key and body.

    Args:
        storage: The mocked storage client.

    Returns:
        Each uploaded key and body.

    """
    return {
        cast("str", cast("list[object]", c.args)[1]): cast(
            "bytes", cast("list[object]", c.args)[2]
        )
        for c in cast("list[object]", cast("AsyncMock", storage.upload).await_args_list)
    }


@pytest.mark.asyncio
async def test_three_chunks_write_six_objects_and_a_redelivery_writes_none() -> None:
    """Test that each chunk becomes a file and a sidecar, and a redelivery writes nothing.

    **Why this test is important:**
      - A ``NONE``-chunking data source embeds each file as one chunk, so one file per
        chunk is what keeps the engine's chunks; an SQS redelivery must not rewrite (and
        re-sync) them.

    **What it tests:**
      - three chunks → six uploads in ``kb-bucket``: ``{n}.txt`` and
        ``{n}.txt.metadata.json`` under ``kb/o1/{hash2}/doc1/{version}/``
      - a sidecar holds the document's and the chunk's attributes, the org, the chunk
        index (a number), the chunk id (``doc1:0``, the id the vector sink gives the same
        chunk, so a citation names its chunk on either backend) and the entities as a
        string list
      - with all six keys listed, a second write uploads and deletes nothing
    """
    storage = _storage()
    await _sink(storage).write(_DOC, _CHUNKS, None)
    written = _uploads(storage)
    redelivered = _storage(list(written))
    await _sink(redelivered).write(_DOC, _CHUNKS, None)

    assert sorted(written) == sorted(
        f"{_PREFIX}{_VERSION}/{n}.txt{suffix}"
        for n in range(3)
        for suffix in ("", ".metadata.json")
    )
    assert written[f"{_PREFIX}{_VERSION}/0.txt"] == b"alpha"
    attributes = cast(
        "object",
        json.loads(written[f"{_PREFIX}{_VERSION}/0.txt.metadata.json"])[
            "metadataAttributes"
        ],
    )
    assert attributes == {
        "classification": "internal",
        "org_id": "o1",
        "document_id": "doc1",
        "content_hash": "c" * 64,
        "chunk_index": 0,
        "chunk_id": "doc1:0",
        "section_path": "Intro",
        "entities.ORG": ["Acme", "Initech"],
    }
    assert all(
        cast("list[object]", c.args)[0] == "kb-bucket"
        for c in cast("list[object]", cast("AsyncMock", storage.upload).await_args_list)
    )
    cast(
        "MagicMock", cast("AsyncMock", redelivered.list_objects).assert_awaited_once_with
    )("kb-bucket", _PREFIX)
    cast("MagicMock", cast("AsyncMock", redelivered.upload).assert_not_awaited)()
    cast("MagicMock", cast("AsyncMock", redelivered.delete).assert_not_awaited)()


@pytest.mark.asyncio
async def test_chunk_file_cites_the_chunk_ordinal() -> None:
    """Test that a chunk file's chunk id and index come from the chunk's ordinal when set.

    **Why this test is important:**
      - The vector sink ids a chunk by its ordinal; a Bedrock citation must name the same
        chunk.

    **What it tests:**
      - a chunk with ordinal 5, written first, has ``chunk_index`` 5 and ``chunk_id``
        ``doc1:5``
    """
    storage = _storage()
    await _sink(storage).write(_DOC, [ChunkPayload("alpha", ordinal=5)], None)

    sidecar = _uploads(storage)[f"{_PREFIX}{_VERSION}/0.txt.metadata.json"]
    attributes = cast("dict[str, object]", json.loads(sidecar)["metadataAttributes"])
    assert (attributes["chunk_index"], attributes["chunk_id"]) == (5, "doc1:5")


@pytest.mark.asyncio
async def test_a_new_version_replaces_the_old_files() -> None:
    """Test that a changed document writes its new version and deletes the old one.

    **Why this test is important:**
      - Skipping present keys must not keep stale text: a document whose content changed
        would otherwise answer from its old chunks.

    **What it tests:**
      - with the old version's two objects listed, a write of the new content uploads six
        new objects and deletes exactly the two old ones
    """
    old = [f"{_PREFIX}{'a' * 16}/0.txt", f"{_PREFIX}{'a' * 16}/0.txt.metadata.json"]
    storage = _storage(old)

    await _sink(storage).write(_DOC, _CHUNKS, None)

    assert (
        len(cast("list[object]", cast("AsyncMock", storage.upload).await_args_list))
        == _EXPECTED_AWAIT_ARGS_LIST_COUNT
    )
    assert sorted(
        cast("str", cast("tuple[object, ...]", c.args)[1])
        for c in cast("list[object]", cast("AsyncMock", storage.delete).await_args_list)
    ) == sorted(old)


@pytest.mark.asyncio
async def test_kb_units_write_one_unit_per_chunk_with_page_sidecars() -> None:
    """Test that ``kb_units`` writes each chunk as its own unit, with its page range.

    **Why this test is important:**
      - Each unit is what Bedrock indexes and cites; packing chunks together lost the
        page range a citation links to, and a page-less chunk must still be indexed.

    **What it tests:**
      - three chunks become units ``0.txt``, ``1.txt`` and ``2.txt`` holding exactly
        their text
      - a chunk carrying ``page_start``/``page_end`` metadata has them in its sidecar as
        numbers, beside the document's attributes and its ``chunk_index``
      - a chunk without page metadata has a ``chunk_index`` and no page fields
    """
    storage = _storage()
    chunks = [
        ChunkPayload("alpha", metadata={"page_start": "1", "page_end": "4"}),
        ChunkPayload("beta", metadata={"page_start": "4", "page_end": "7"}),
        ChunkPayload("gamma"),
    ]

    await _sink(storage, BedrockLayout.KB_UNITS).write(_DOC, chunks, None)

    written = _uploads(storage)
    assert [written[f"{_PREFIX}{_VERSION}/{n}.txt"] for n in range(3)] == [
        b"alpha",
        b"beta",
        b"gamma",
    ]

    def attributes(n: int) -> dict[str, object]:
        body = cast(
            "dict[str, object]",
            json.loads(written[f"{_PREFIX}{_VERSION}/{n}.txt.metadata.json"]),
        )
        return cast("dict[str, object]", dig(body, "metadataAttributes"))

    paged = attributes(1)
    assert (paged["page_start"], paged["page_end"], paged["chunk_index"]) == (4, 7, 1)
    assert paged["document_id"] == "doc1"
    assert paged["classification"] == "internal"
    pageless = attributes(2)
    assert pageless["chunk_index"] == _PAGELESS_INDEX
    assert "page_start" not in pageless
    assert "page_end" not in pageless


@pytest.mark.asyncio
async def test_kb_units_refuse_a_chunk_over_the_size_limit() -> None:
    """Test that a chunk bigger than ``max_unit_bytes`` fails the write.

    **Why this test is important:**
      - Bedrock rejects an over-size file only after ingestion, as a per-file failure
        that is easy to miss; refusing it before any upload names the cause and leaves
        the previous version intact.

    **What it tests:**
      - with a 12-byte ceiling, a 13-byte chunk raises ``AppValueError`` naming
        ``max_unit_bytes``, and nothing is uploaded
    """
    storage = _storage()

    with pytest.raises(AppValueError, match="max_unit_bytes"):
        await _sink(storage, BedrockLayout.KB_UNITS).write(
            _DOC, [ChunkPayload("x" * 13)], None
        )

    cast("AsyncMock", storage.upload).assert_not_awaited()


@pytest.mark.asyncio
@pytest.mark.parametrize("layout", list(BedrockLayout))
async def test_a_write_with_no_chunks_is_refused_and_touches_nothing(
    layout: BedrockLayout,
) -> None:
    """Test that a write with no chunks fails before any storage call.

    **Why this test is important:**
      - A zero-chunk write would keep no files, so it deleted the document's previous
        version and, under ``kb_units``, wrote a manifest declaring zero units: the
        caller then indexed a document with no text, which reads as indexed but can
        never be cited. Refusing it lets the caller fail the document instead.

    **What it tests:**
      - in every layout, an empty chunk list raises ``AppValueError`` naming the
        zero units, and nothing is listed, uploaded or deleted, so a previous version
        survives

    Args:
        layout: The sink layout under test.

    """
    storage = _storage([f"{_PREFIX}old.txt"])

    with pytest.raises(AppValueError, match="zero units"):
        await _sink(storage, layout).write(_DOC, [], None)

    cast("AsyncMock", storage.list_objects).assert_not_awaited()
    cast("AsyncMock", storage.upload).assert_not_awaited()
    cast("AsyncMock", storage.delete).assert_not_awaited()


@pytest.mark.asyncio
async def test_kb_units_write_the_manifest_last_and_keep_it_on_a_rerun() -> None:
    """Test that the version's manifest is written after every unit and survives a rerun.

    **Why this test is important:**
      - The indexer trusts a version only once its manifest exists; written last, a
        crash mid-write leaves no manifest, so a partial set of units is never indexed.

    **What it tests:**
      - the last upload is ``{version}/manifest.json`` holding the content hash and the
        unit count
      - a rerun with the manifest already stored writes it again and never deletes it,
        while an old version's manifest is deleted
      - ``unit_prefix`` names the version directory the units and manifest live in
    """
    storage = _storage()

    await _sink(storage, BedrockLayout.KB_UNITS).write(_DOC, _CHUNKS, None)

    version = unit_prefix("kb", "o1", "doc1", _DOC.content_hash)
    assert version == f"{_PREFIX}{_VERSION}/"
    calls = cast("list[object]", cast("AsyncMock", storage.upload).await_args_list)
    last = cast("tuple[object, ...]", cast("AsyncMock", calls[-1]).args)
    assert last[1] == f"{version}manifest.json"
    assert json.loads(cast("bytes", last[2])) == {
        "content_hash": _DOC.content_hash,
        "unit_count": len(_CHUNKS),
    }

    old_manifest = f"{_PREFIX}{'a' * 16}/manifest.json"
    rerun = _storage([*_uploads(storage), old_manifest])
    await _sink(rerun, BedrockLayout.KB_UNITS).write(_DOC, _CHUNKS, None)

    assert list(_uploads(rerun)) == [f"{version}manifest.json"]
    deleted = [
        cast("tuple[object, ...]", c.args)[1]
        for c in cast("list[object]", cast("AsyncMock", rerun.delete).await_args_list)
    ]
    assert deleted == [old_manifest]


@pytest.mark.asyncio
async def test_delete_removes_every_object_and_update_rewrites_only_sidecars() -> None:
    """Test that delete removes the whole document.

    A metadata update touches only sidecars.

    **Why this test is important:**
      - A deleted document must leave no file Bedrock could still serve; a clearance
        change must not rewrite (and re-embed) the text.

    **What it tests:**
      - ``delete`` lists the document's prefix and deletes every object under it
      - ``update_metadata`` downloads and rewrites the two sidecars with the new field
        merged in, and never uploads a ``.txt``
    """
    keys = [
        f"{_PREFIX}{_VERSION}/{n}.txt{s}"
        for n in range(2)
        for s in ("", ".metadata.json")
    ]
    storage = _storage(keys)
    storage.download = AsyncMock(
        return_value=json.dumps({
            "metadataAttributes": {"classification": "internal", "chunk_index": 0}
        }).encode()
    )
    sink = _sink(storage)

    await sink.delete("o1", "doc1")
    await sink.update_metadata("o1", "doc1", {"classification": "secret"})

    assert sorted(
        cast("str", cast("tuple[object, ...]", c.args)[1])
        for c in cast("list[object]", cast("AsyncMock", storage.delete).await_args_list)
    ) == sorted(keys)
    rewritten = _uploads(storage)
    assert sorted(rewritten) == sorted(k for k in keys if k.endswith(".metadata.json"))
    assert json.loads(next(iter(rewritten.values())))["metadataAttributes"] == {
        "classification": "secret",
        "chunk_index": 0,
    }


@pytest.mark.asyncio
async def test_every_written_object_carries_the_configured_tags() -> None:
    """Test that the sink tags each object it writes, a sidecar rewrite included.

    **Why this test is important:**
      - A bucket lifecycle rule can select objects only by a fixed key prefix or by tag;
        a sink whose files sit under per-tenant prefixes is reachable only by tag, so an
        untagged object falls under the bucket's default rule instead.

    **What it tests:**
      - with ``object_tags`` set, every unit and sidecar ``write`` uploads and every
        sidecar ``update_metadata`` rewrites is uploaded with exactly those tags
      - with no ``object_tags`` (the default), uploads pass no tags
    """
    tags = {"sink": "bedrock"}
    sidecar_key = f"{_PREFIX}{_VERSION}/0.txt.metadata.json"
    storage = _storage([sidecar_key])
    storage.download = AsyncMock(return_value=b'{"metadataAttributes": {}}')
    sink = _sink(storage, object_tags=tags)

    await sink.write(_DOC, _CHUNKS, None)
    await sink.update_metadata("o1", "doc1", {"classification": "secret"})
    untagged = _storage()
    await _sink(untagged).write(_DOC, _CHUNKS, None)

    def _tags(mock: MagicMock) -> list[dict[str, object]]:
        calls = cast("list[object]", cast("AsyncMock", mock.upload).await_args_list)
        return [cast("dict[str, object]", c.kwargs) for c in calls]

    # The write skips the listed sidecar (5 uploads); the rewrite uploads it (1).
    assert _tags(storage) == [{"tags": tags}] * _EXPECTED_AWAIT_ARGS_LIST_COUNT
    assert _tags(untagged) == [{}] * _EXPECTED_AWAIT_ARGS_LIST_COUNT


@pytest.mark.asyncio
async def test_bedrock_sink_refuses_what_it_cannot_write() -> None:
    """Test that a misconfigured or misused bedrock sink fails with ``INVALID_INPUT``.

    **Why this test is important:**
      - A sink without a bucket fails on its first write, far from the config; vectors
        passed to a sink that embeds by itself mean the pipeline is wired for another
        sink.

    **What it tests:**
      - no storage, no bucket, a zero concurrency → ``AppValueError`` at build
      - vectors, and a document id containing ``/``, → ``AppValueError`` at write
      - the default ``SinkConfig`` still builds the noop sink
    """
    for config, storage in (
        (SinkConfig(kind=SinkKind.BEDROCK, bedrock=BedrockSinkConfig(bucket="b")), None),
        (SinkConfig(kind=SinkKind.BEDROCK), _storage()),
        (
            SinkConfig(
                kind=SinkKind.BEDROCK,
                bedrock=BedrockSinkConfig(bucket="b", concurrency=0),
            ),
            _storage(),
        ),
    ):
        with pytest.raises(AppValueError):
            _ = sink_from_config(config, storage=storage)
    sink = _sink(_storage())
    slashed = SinkDocument("o1", "a/b", "c" * 64, "", "")

    with pytest.raises(AppValueError, match="embeds by itself"):
        await sink.write(_DOC, _CHUNKS, [[0.1]] * 3)
    with pytest.raises(AppValueError, match="no '/'"):
        await sink.write(slashed, _CHUNKS, None)
    assert type(sink_from_config(SinkConfig())).__name__ == "NoopSink"
