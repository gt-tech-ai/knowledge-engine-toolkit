"""Unit tests for the checkpoint stores.

(``clients.checkpoint``) with the object store mocked.
"""

from __future__ import annotations

import gzip
import threading
from datetime import UTC, datetime
from typing import cast
from unittest.mock import AsyncMock, call

import pytest

from techai_webutils.clients.checkpoint import (
    CheckpointCompression,
    CheckpointConfig,
    CheckpointKind,
    checkpoint_store_from_config,
)
from techai_webutils.clients.checkpoint.s3db.layout import Layout, decode, encode, key_of
from techai_webutils.core.errors import AppValueError, ConflictError
from techai_webutils.core.interfaces.checkpoint import (
    CheckpointKey,
    CheckpointStore,
    Stage,
    StageArtifact,
    StageState,
)
from techai_webutils.core.interfaces.storage import StorageClient, StorageObject

_STAMP = datetime(2026, 11, 5, tzinfo=UTC)
_KEY = CheckpointKey("org-1", "doc-1", "c0ffee", Stage.PARSE, "abcdef1234567890")
_LAYOUT = Layout("checkpoints", "part")
_BASE = "checkpoints/c0/org-1/c0ffee/parse-abcdef123456"
_ENVELOPE = f"{_BASE}.json.gz"
_ZSTD_MAGIC = b"\x28\xb5\x2f\xfd"


def _object(key: str) -> StorageObject:
    """Return a listed object at ``key``."""
    return StorageObject(key=key, size=0, content_type="", last_modified="", etag="")


def _store(
    objects: AsyncMock,
    states: AsyncMock,
    max_bytes: int = 1024,
    compression: CheckpointCompression = CheckpointCompression.GZIP,
) -> CheckpointStore:
    """Build the s3db store over the mocked object store and state client."""
    config = CheckpointConfig(
        kind=CheckpointKind.S3DB,
        bucket="ckpt",
        max_artifact_bytes=max_bytes,
        compression=compression,
    )
    return checkpoint_store_from_config(
        config, object_store=objects, state_client=states, clock=lambda: _STAMP
    )


def _serve(objects: AsyncMock, bodies: dict[str, bytes]) -> None:
    """Make the mocked object store answer ``exists`` / ``download`` from ``bodies``."""

    def exists(_bucket: str, key: str) -> bool:
        return key in bodies

    def download(_bucket: str, key: str) -> bytes:
        return bodies[key]

    objects.exists.side_effect = exists
    objects.download.side_effect = download


class TestBuilder:
    """``checkpoint_store_from_config`` selects and validates the Kind."""

    @pytest.mark.asyncio
    async def test_noop_is_the_default_and_always_misses(self) -> None:
        """The default store keeps nothing.

        **Why this test is important:**
          - Checkpointing is opt-in; with the default config a pipeline must run every
            stage, as it does today, and pay for no storage.

        **What it tests:**
          - The default config builds a store whose ``get``, ``get_part`` and ``parts``
            miss after a ``put`` and a ``put_part``, and whose ``delete`` / ``purge`` do
            nothing.
        """
        store = checkpoint_store_from_config(CheckpointConfig())

        await store.put(StageArtifact(_KEY, b"x"))
        await store.put_part(_KEY, 1, b"p")
        await store.delete("org-1", "doc-1", [Stage.PARSE])
        await store.purge("org-1", "doc-1")

        assert await store.get(_KEY) is None
        assert await store.get_part(_KEY, 1) is None
        assert await store.parts(_KEY) == frozenset()

    def test_s3db_needs_its_dependencies_and_unknown_kinds_fail(self) -> None:
        """``s3db`` without a bucket, object store or state client, or an unknown kind.

        Is refused.

        **Why this test is important:**
          - A misconfigured store must stop the service at boot, not fail on the first
            document.

        **What it tests:**
          - Each missing dependency and an unknown kind raise ``AppValueError``.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        s3db = CheckpointConfig(kind=CheckpointKind.S3DB, bucket="b")
        with pytest.raises(AppValueError):
            checkpoint_store_from_config(s3db, state_client=states)
        with pytest.raises(AppValueError):
            checkpoint_store_from_config(s3db, object_store=objects)
        with pytest.raises(AppValueError):
            checkpoint_store_from_config(
                CheckpointConfig(kind=CheckpointKind.S3DB),
                object_store=objects,
                state_client=states,
            )
        with pytest.raises(AppValueError):
            checkpoint_store_from_config(
                CheckpointConfig(kind=cast("CheckpointKind", "other"))
            )


class TestS3DbWrite:
    """``put`` and ``put_part`` on the s3db store."""

    @pytest.mark.asyncio
    async def test_put_writes_envelope_markers_and_state(self) -> None:
        """A new artifact is written once, referenced, and its stage recorded.

        **Why this test is important:**
          - The planner trusts the stage record and purge trusts the markers; a write that
            missed either would rerun stages forever or delete artifacts still in use.

        **What it tests:**
          - ``put`` uploads the envelope (header + payload), the holder and reference
            markers, and records a ``StageState`` with the artifact key, the clock's time
            and the attempt.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        _serve(objects, {})
        artifact = StageArtifact(
            _KEY, b'{"text":"hi"}', versions={"parser": "2"}, attempt=3
        )

        await _store(objects, states).put(artifact)

        uploads = {c.args[1]: c.args[2] for c in objects.upload.await_args_list}
        header, payload = decode(uploads[_ENVELOPE])
        assert payload == b'{"text":"hi"}'
        assert header == {
            "format_version": 1,
            "parts_done": [],
            "segments": 0,
            "versions": {"parser": "2"},
        }
        assert uploads[f"{_BASE}.refs/doc-1"] == b""
        assert uploads["checkpoints/refs/org-1/doc-1/c0ffee-parse-abcdef123456"] == b""
        states.put_state.assert_awaited_once_with(
            StageState(
                "org-1",
                "doc-1",
                Stage.PARSE,
                "c0ffee",
                _KEY.fingerprint,
                _ENVELOPE,
                _STAMP,
                3,
            )
        )

    @pytest.mark.asyncio
    async def test_put_reuses_a_stored_artifact(self) -> None:
        """A second document with the same bytes adds only its markers.

        **Why this test is important:**
          - Duplicate uploads of one file are common; re-writing a large shared artifact
            for each would pay its upload cost again for nothing.

        **What it tests:**
          - When the envelope exists, ``put`` uploads only the two markers.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        _serve(objects, {_ENVELOPE: encode({"format_version": 1}, b"x")})

        await _store(objects, states).put(StageArtifact(_KEY, b"x"))

        written = [c.args[1] for c in objects.upload.await_args_list]
        assert _ENVELOPE not in written
        assert len(written) == 2

    @pytest.mark.asyncio
    async def test_payload_over_the_cap_is_segmented_before_the_envelope(self) -> None:
        """A large payload is written as capped segments, then an envelope naming them.

        **Why this test is important:**
          - One object per huge artifact would outgrow single-PUT limits; writing the
            envelope first would let a reader find it before its segments exist.

        **What it tests:**
          - A 2,500-byte payload with a 1,024-byte cap uploads segments 0, 1, 2 (gzip of
            1,024, 1,024 and 452 bytes) and then the envelope, whose header says three
            segments.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        _serve(objects, {})
        payload = bytes(range(250)) * 10

        await _store(objects, states, max_bytes=1024).put(StageArtifact(_KEY, payload))

        uploads = [(c.args[1], c.args[2]) for c in objects.upload.await_args_list]
        names = [name for name, _ in uploads]
        assert names[:4] == [f"{_BASE}.seg-{n}.gz" for n in range(3)] + [_ENVELOPE]
        assert [len(gzip.decompress(body)) for _, body in uploads[:3]] == [
            1024,
            1024,
            452,
        ]
        header, inline = decode(uploads[3][1])
        assert header["segments"] == 3
        assert inline == b""

    @pytest.mark.asyncio
    async def test_parts_round_trip(self) -> None:
        """Parts are written gzipped, listed by number, and read back.

        **Why this test is important:**
          - A stage that writes as it goes (page by page) resumes from its last part; a
            wrong part list would redo or skip pages.

        **What it tests:**
          - ``put_part`` uploads ``…/part-7.json.gz``; ``parts`` parses the listed numbers
            2 and 7; ``get_part`` decompresses a stored part and misses an absent one.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        part = f"{_BASE}/part-7.json.gz"
        _serve(objects, {part: gzip.compress(b"page")})
        objects.list_objects.return_value = [
            _object(f"{_BASE}/part-2.json.gz"),
            _object(part),
        ]
        store = _store(objects, states)

        await store.put_part(_KEY, 7, b"page")

        assert objects.upload.await_args_list[0].args[1] == part
        assert await store.parts(_KEY) == frozenset({2, 7})
        objects.list_objects.assert_awaited_with("ckpt", f"{_BASE}/part-")
        assert await store.get_part(_KEY, 7) == b"page"
        assert await store.get_part(_KEY, 8) is None


class TestS3DbCompression:
    """The s3db store's configurable compression."""

    @pytest.mark.asyncio
    async def test_zstd_round_trips_artifacts_segments_and_parts(self) -> None:
        """A store configured for zstd writes zstd everywhere and reads it back.

        **Why this test is important:**
          - zstd is selected to cut the CPU spent on every stage hand-off, so an object
            left in gzip would silently lose that, and one mislabelled would mislead
            anything reading the bucket.

        **What it tests:**
          - Segments, the envelope and a part all upload as zstd frames with the zstd
            content type, under the unchanged object keys, and ``get`` / ``get_part``
            return the payloads.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        _serve(objects, {})
        store = _store(objects, states, 4, CheckpointCompression.ZSTD)

        await store.put(StageArtifact(_KEY, b"0123456789"))
        await store.put_part(_KEY, 7, b"page")

        written = [c.args for c in objects.upload.await_args_list if c.args[2]]
        assert len(written) == 5
        assert all(args[2].startswith(_ZSTD_MAGIC) for args in written)
        assert {args[3] for args in written} == {"application/zstd"}
        assert _ENVELOPE in {args[1] for args in written}
        _serve(objects, {c.args[1]: c.args[2] for c in objects.upload.await_args_list})
        stored = await store.get(_KEY)
        assert stored is not None
        assert stored.payload == b"0123456789"
        assert await store.get_part(_KEY, 7) == b"page"

    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("writer", "reader"),
        [
            (CheckpointCompression.GZIP, CheckpointCompression.ZSTD),
            (CheckpointCompression.ZSTD, CheckpointCompression.GZIP),
        ],
        ids=["gzip-read-by-zstd", "zstd-read-by-gzip"],
    )
    async def test_a_store_reads_either_compression(
        self, writer: CheckpointCompression, reader: CheckpointCompression
    ) -> None:
        """An artifact is readable whichever compression the reading store is configured.

        With.

        **Why this test is important:**
          - During a rollout, or after turning zstd back off, a bucket holds both formats
            under the same keys; a store that trusted its own setting would rerun every
            older stage.

        **What it tests:**
          - An artifact and part written under one compression are returned by a store
            configured with the other.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        _serve(objects, {})
        first = _store(objects, states, compression=writer)
        await first.put(StageArtifact(_KEY, b"payload"))
        await first.put_part(_KEY, 1, b"page")
        _serve(objects, {c.args[1]: c.args[2] for c in objects.upload.await_args_list})

        second = _store(objects, states, compression=reader)
        stored = await second.get(_KEY)

        assert stored is not None
        assert stored.payload == b"payload"
        assert await second.get_part(_KEY, 1) == b"page"

    def test_an_unknown_compression_fails_at_boot(self) -> None:
        """An unknown compression stops the store being built.

        **Why this test is important:**
          - Discovering it on the first checkpoint write would fail documents in
            production instead of failing the deploy.

        **What it tests:**
          - An unknown value raises ``AppValueError``.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        with pytest.raises(AppValueError):
            _store(objects, states, compression=cast("CheckpointCompression", "brotli"))


class TestS3DbEventLoop:
    """The s3db store keeps gzip work off the event loop."""

    @pytest.mark.asyncio
    async def test_gzip_never_runs_on_the_event_loop_thread(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Every compress and decompress of an artifact or part runs on a worker thread.

        **Why this test is important:**
          - gzip takes hundreds of milliseconds on a multi-megabyte artifact. On the event
            loop that stalls every other task in the process, including message-lease
            heartbeats and health checks, so a large document could cost another document
            its lease.

        **What it tests:**
          - ``put`` (segments and envelope), ``put_part``, ``get`` (envelope and segments)
            and ``get_part`` all gzip away from the thread running the loop, and the
            payloads still round-trip.
        """
        loop_thread = threading.get_ident()
        threads: list[int] = []
        real_compress, real_decompress = gzip.compress, gzip.decompress

        def compress(data: bytes) -> bytes:
            threads.append(threading.get_ident())
            return real_compress(data)

        def decompress(data: bytes) -> bytes:
            threads.append(threading.get_ident())
            return real_decompress(data)

        monkeypatch.setattr(gzip, "compress", compress)
        monkeypatch.setattr(gzip, "decompress", decompress)
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        _serve(objects, {})
        store = _store(objects, states, max_bytes=4)

        await store.put(StageArtifact(_KEY, b"0123456789"))
        await store.put_part(_KEY, 7, b"page")
        _serve(objects, {c.args[1]: c.args[2] for c in objects.upload.await_args_list})
        stored = await store.get(_KEY)

        assert stored is not None
        assert stored.payload == b"0123456789"
        assert await store.get_part(_KEY, 7) == b"page"
        assert threads
        assert loop_thread not in threads


class TestS3DbRead:
    """``get`` on the s3db store."""

    @pytest.mark.asyncio
    async def test_get_reads_inline_and_segmented_payloads(self) -> None:
        """An envelope's payload, versions and parts come back.

        Segments are joined in order.

        **Why this test is important:**
          - A stage reuses its stored output instead of running; a mangled payload or
            version map would feed the next stage wrong data.

        **What it tests:**
          - An inline envelope returns its payload, versions and ``parts_done``; an
            envelope naming two segments returns their concatenation; an absent key is
            ``None``.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        inline = encode(
            {"format_version": 1, "versions": {"p": "1"}, "parts_done": [3]}, b"data"
        )
        _serve(objects, {_ENVELOPE: inline})
        store = _store(objects, states)

        got = await store.get(_KEY)

        assert got == StageArtifact(
            _KEY, b"data", versions={"p": "1"}, parts_done=frozenset({3})
        )
        _serve(
            objects,
            {
                _ENVELOPE: encode({"format_version": 1, "segments": 2}, b""),
                f"{_BASE}.seg-0.gz": gzip.compress(b"ab"),
                f"{_BASE}.seg-1.gz": gzip.compress(b"cd"),
            },
        )
        segmented = await store.get(_KEY)
        assert segmented is not None
        assert segmented.payload == b"abcd"
        _serve(objects, {})
        assert await store.get(_KEY) is None

    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        "body",
        [
            b"not gzip",
            gzip.compress(b"no header line"),
            gzip.compress(b"[1]\nx"),
            encode({}, b""),
            _ZSTD_MAGIC + b"garbage",
        ],
        ids=["bad-gzip", "no-header", "header-not-object", "no-version", "bad-zstd"],
    )
    async def test_a_corrupt_envelope_is_a_miss(self, body: bytes) -> None:
        """A corrupt envelope reads as a miss, so the stage simply runs again.

        **Why this test is important:**
          - A truncated or garbled object must not fail the document forever; recomputing
            the stage repairs it.

        **What it tests:**
          - Bad gzip or zstd, a missing header line, a non-object header and a header with
            no version each make ``get`` return ``None``.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        _serve(objects, {_ENVELOPE: body})

        assert await _store(objects, states).get(_KEY) is None

    @pytest.mark.asyncio
    async def test_format_versions(self) -> None:
        """The store reads its format and the one before, refuses newer, and misses older.

        **Why this test is important:**
          - During a rolling deploy an older reader meets newer artifacts; silently
            misreading them would corrupt output, while refusing older ones after an
            upgrade would rerun every document.

        **What it tests:**
          - Format 1 and 0 are read; format 2 raises ``ConflictError``; format -1 is a
            miss.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        store = _store(objects, states)
        for version, readable in ((1, True), (0, True), (-1, False)):
            _serve(objects, {_ENVELOPE: encode({"format_version": version}, b"x")})
            assert (await store.get(_KEY) is not None) is readable
        _serve(objects, {_ENVELOPE: encode({"format_version": 2}, b"x")})
        with pytest.raises(ConflictError):
            await store.get(_KEY)


class TestS3DbDelete:
    """``delete`` and ``purge`` release references and delete unshared artifacts."""

    @pytest.mark.asyncio
    async def test_purge_deletes_an_artifact_no_one_else_holds(self) -> None:
        """Purging the last holder deletes the artifact and everything beside it.

        **Why this test is important:**
          - A deleted document's derived text must not outlive it; leaving its artifacts
            behind keeps customer content after deletion.

        **What it tests:**
          - With one reference and no other holder, ``purge`` deletes the holder marker,
            the reference, and every object under the artifact's stem.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        ref = "checkpoints/refs/org-1/doc-1/c0ffee-parse-abcdef123456"
        stem = [_object(_ENVELOPE), _object(f"{_BASE}/part-1.json.gz")]
        objects.list_objects.side_effect = [[_object(ref)], [], stem]

        await _store(objects, states).purge("org-1", "doc-1")

        assert objects.delete.await_args_list == [
            call("ckpt", f"{_BASE}.refs/doc-1"),
            call("ckpt", ref),
            call("ckpt", _ENVELOPE),
            call("ckpt", f"{_BASE}/part-1.json.gz"),
        ]

    @pytest.mark.asyncio
    async def test_delete_keeps_a_shared_artifact_and_other_stages(self) -> None:
        """Deleting one stage for one document keeps what others still hold.

        **Why this test is important:**
          - Artifacts are shared by equal bytes within an org; deleting one document's
            copy must not break another document's reuse, and a stage delete must leave
            other stages.

        **What it tests:**
          - With a parse and a chunk reference and another holder of parse, ``delete`` of
            parse removes only that document's parse markers; the chunk reference is
            untouched.
        """
        objects, states = AsyncMock(spec=StorageClient), AsyncMock()
        parse_ref = "checkpoints/refs/org-1/doc-1/c0ffee-parse-abcdef123456"
        chunk_ref = "checkpoints/refs/org-1/doc-1/c0ffee-chunk-0123456789ab"
        other_holder = _object(f"{_BASE}.refs/doc-2")
        objects.list_objects.side_effect = [
            [_object(parse_ref), _object(chunk_ref)],
            [other_holder],
        ]

        await _store(objects, states).delete("org-1", "doc-1", [Stage.PARSE])

        assert objects.delete.await_args_list == [
            call("ckpt", f"{_BASE}.refs/doc-1"),
            call("ckpt", parse_ref),
        ]


def test_layout_recovers_a_key_from_its_reference() -> None:
    """A reference marker names its artifact's stem.

    **Why this test is important:**
      - Purge finds a document's artifacts only through its references; a reference that
        did not map back to the artifact's stem would orphan the artifact.

    **What it tests:**
      - ``key_of(reference(key))`` has the key's stage and content hash, and the same
        stem.
    """
    key = key_of(_LAYOUT.reference(_KEY), "org-1", "doc-1")

    assert (key.stage, key.content_hash) == (Stage.PARSE, "c0ffee")
    assert _LAYOUT.base(key) == _BASE


def test_new_objects_default_to_zstd() -> None:
    """Keep the cheaper codec the default.

    Why this test is important: Compressing every checkpoint with gzip costs several times
        the CPU for the same size.

    What it tests: A config that names no compression selects zstd.

    """
    assert CheckpointConfig().compression is CheckpointCompression.ZSTD
