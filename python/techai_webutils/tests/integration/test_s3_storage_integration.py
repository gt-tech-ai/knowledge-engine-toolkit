"""Integration tests for S3StorageClient against real MinIO.

Unit tests mock aiobotocore and assert call shapes; this suite proves the real
object lifecycle (upload -> download -> exists -> list -> delete) over an
S3-compatible store, including streaming-body download which mocks gloss over.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import pytest

from techai_webutils.clients.storage.s3.s3_client import S3StorageClient

if TYPE_CHECKING:
    from pathlib import Path

    from techai_webutils.clients.storage.config import S3Config


@pytest.mark.integration
@pytest.mark.asyncio
async def test_upload_then_download_roundtrip(s3_config: S3Config) -> None:
    """Test that uploaded bytes are returned verbatim by download.

    **Why this test is important:**
      - Document binaries flow through this client; a corrupted or truncated
        round-trip silently damages stored content. The streaming-body read in
        download() is real I/O that a mock cannot validate

    **What it tests:**
      - download returns exactly the bytes uploaded
    """
    async with S3StorageClient(s3_config) as storage:
        await storage.upload(s3_config.bucket, "docs/a.txt", b"hello-s3", "text/plain")

        assert await storage.download(s3_config.bucket, "docs/a.txt") == b"hello-s3"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_download_to_path_streams_large_object_to_disk(
    s3_config: S3Config, tmp_path: Path
) -> None:
    """Test that download_to_path streams a multi-chunk object to disk byte-exact.

    **Why this test is important:**
      - The large-document lane never pulls the object through memory; it streams the
        real aiobotocore ``StreamingBody`` to a scratch file. That body is a wrapt proxy over
        aiohttp's ``ClientResponse`` — it exposes ``read(amt)``, not the sync botocore
        ``iter_chunks``, and its ``__aenter__`` yields the wrapped response. A unit mock cannot
        model those quirks, so only a real-store download proves the streaming read is correct;
        a regression to ``iter_chunks`` (or reading off the ``async with`` binding) fails here.

    **What it tests:**
      - A payload larger than the 1 MiB streaming chunk (forcing several ``read`` iterations plus a
        final partial read) is written to ``path`` exactly, byte-for-byte.
    """
    payload = b"large-lane-" * 131_072  # ~2.75 MiB -> 3 chunked reads + a partial tail
    dest = tmp_path / "streamed.bin"
    async with S3StorageClient(s3_config) as storage:
        await storage.upload(
            s3_config.bucket, "docs/large.bin", payload, "application/octet-stream"
        )

        await storage.download_to_path(s3_config.bucket, "docs/large.bin", str(dest))

    assert dest.read_bytes() == payload


@pytest.mark.integration
@pytest.mark.asyncio
async def test_exists_reflects_object_presence(s3_config: S3Config) -> None:
    """Test that exists is False for a missing key and True after upload.

    **Why this test is important:**
      - exists drives idempotency and skip logic; it relies on a head_object whose
        404-vs-200 handling can only be confirmed against a real store

    **What it tests:**
      - exists is False before upload and True after
    """
    async with S3StorageClient(s3_config) as storage:
        assert await storage.exists(s3_config.bucket, "docs/missing.txt") is False

        await storage.upload(s3_config.bucket, "docs/present.txt", b"x", "text/plain")
        assert await storage.exists(s3_config.bucket, "docs/present.txt") is True


@pytest.mark.integration
@pytest.mark.asyncio
async def test_delete_removes_object(s3_config: S3Config) -> None:
    """Test that delete removes an object so exists returns False.

    **Why this test is important:**
      - Deletion is the retention/cleanup path; a no-op delete leaks storage and
        leaves data that should be gone

    **What it tests:**
      - The object exists after upload and is absent after delete
    """
    async with S3StorageClient(s3_config) as storage:
        await storage.upload(s3_config.bucket, "docs/tmp.txt", b"bye", "text/plain")
        assert await storage.exists(s3_config.bucket, "docs/tmp.txt") is True

        await storage.delete(s3_config.bucket, "docs/tmp.txt")

        assert await storage.exists(s3_config.bucket, "docs/tmp.txt") is False


@pytest.mark.integration
@pytest.mark.asyncio
async def test_list_objects_filters_by_prefix(s3_config: S3Config) -> None:
    """Test that list_objects returns only keys under the requested prefix.

    **Why this test is important:**
      - Prefix listing backs per-document and per-tenant enumeration; a wrong
        prefix filter would leak unrelated objects or miss expected ones

    **What it tests:**
      - Two keys under "docs/" are listed for prefix "docs/"
      - A key under "other/" is excluded from that listing
    """
    async with S3StorageClient(s3_config) as storage:
        await storage.upload(s3_config.bucket, "docs/one.txt", b"1", "text/plain")
        await storage.upload(s3_config.bucket, "docs/two.txt", b"2", "text/plain")
        await storage.upload(s3_config.bucket, "other/three.txt", b"3", "text/plain")

        listed = await storage.list_objects(s3_config.bucket, "docs/")

    keys = {obj.key for obj in listed}
    assert keys == {"docs/one.txt", "docs/two.txt"}
