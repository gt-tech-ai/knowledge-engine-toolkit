"""Tests for S3 storage client with mocked aiobotocore."""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.storage.s3.s3_client import S3StorageClient
from techai_webutils.core.errors import AppRuntimeError

if TYPE_CHECKING:
    from techai_webutils.clients.storage.config import S3Config


async def _open(config: S3Config) -> S3StorageClient:
    """Open a client on the ``aws_client`` session; it stays open, as the session is a mock."""
    return await AsyncExitStack().enter_async_context(S3StorageClient(config))


class TestS3StorageClient:
    """Test suite for S3StorageClient storage operations."""

    @pytest.mark.asyncio
    async def test_upload(self, s3_config: S3Config, aws_client: AsyncMock) -> None:
        """Test that upload calls put_object with the correct bucket, key, body, and content type.

        **Why this test is important:**
          - Document upload is the entry point for the entire ingestion pipeline
          - Incorrect bucket or key would store documents in the wrong location
          - Content type is required for proper MIME handling during retrieval

        **What it tests:**
          - put_object is called once with Bucket, Key, Body, and ContentType
        """
        mock_client = aws_client
        client = await _open(s3_config)

        await client.upload("bucket", "key.txt", b"content", "text/plain")
        mock_client.put_object.assert_called_once_with(
            Bucket="bucket", Key="key.txt", Body=b"content", ContentType="text/plain"
        )

    @pytest.mark.asyncio
    async def test_delete(self, s3_config: S3Config, aws_client: AsyncMock) -> None:
        """Test that delete calls delete_object with the correct bucket and key.

        **Why this test is important:**
          - Document deletion must remove the correct object to comply with data retention policies
          - Incorrect key would leave orphaned objects or delete wrong documents
          - GDPR data deletion requests depend on accurate S3 object removal

        **What it tests:**
          - delete_object is called once with Bucket and Key
        """
        mock_client = aws_client
        client = await _open(s3_config)

        await client.delete("bucket", "key.txt")
        mock_client.delete_object.assert_called_once_with(Bucket="bucket", Key="key.txt")

    @pytest.mark.asyncio
    async def test_exists_true(self, s3_config: S3Config, aws_client: AsyncMock) -> None:
        """Test that exists returns True when the object is found in S3.

        **Why this test is important:**
          - Existence checks prevent duplicate uploads and validate document references
          - A service checks existence before allowing a document download
          - Incorrect existence checks would cause 404 errors or allow ghost references

        **What it tests:**
          - exists() returns True when head_object succeeds
        """
        mock_client = aws_client
        mock_client.head_object = AsyncMock(return_value={})
        client = await _open(s3_config)

        assert await client.exists("bucket", "key") is True

    @pytest.mark.asyncio
    async def test_exists_false(self, s3_config: S3Config, aws_client: AsyncMock) -> None:
        """Test that exists returns False when the object is not found in S3.

        **Why this test is important:**
          - Missing objects must return False rather than raising exceptions
          - Callers use the boolean return to branch between upload and skip logic
          - S3 NoSuchKey errors must be caught and converted to False

        **What it tests:**
          - exists() returns False when head_object raises an exception
        """
        mock_client = aws_client
        mock_client.head_object = AsyncMock(side_effect=Exception("not found"))
        client = await _open(s3_config)

        assert await client.exists("bucket", "key") is False

    @pytest.mark.asyncio
    async def test_list_objects(self, s3_config: S3Config, aws_client: AsyncMock) -> None:
        """Test that list_objects parses S3 response Contents into structured objects.

        **Why this test is important:**
          - Listing a prefix's documents depends on correct S3 object enumeration
          - The response must be parsed into structured objects with key, size, and metadata
          - Incorrect parsing would show wrong file names or sizes in the UI

        **What it tests:**
          - Returned list contains exactly 2 objects
          - First object key equals "a.txt"
          - Second object size equals 200
        """
        mock_client = aws_client
        mock_client.list_objects_v2 = AsyncMock(
            return_value={
                "Contents": [
                    {
                        "Key": "a.txt",
                        "Size": 100,
                        "LastModified": "2024-01-01",
                        "ETag": "e1",
                    },
                    {
                        "Key": "b.txt",
                        "Size": 200,
                        "LastModified": "2024-01-02",
                        "ETag": "e2",
                    },
                ]
            }
        )
        client = await _open(s3_config)

        objects = await client.list_objects("bucket", "prefix/")
        assert len(objects) == 2
        assert objects[0].key == "a.txt"
        assert objects[1].size == 200

    @pytest.mark.asyncio
    async def test_raises_when_not_initialized(self, s3_config: S3Config) -> None:
        """Test that operations raise RuntimeError before the client is initialized.

        **Why this test is important:**
          - S3 client requires async initialization before use
          - Operating on a None client would cause cryptic AttributeError
          - Fail-fast with clear message enables quick diagnosis of startup ordering bugs

        **What it tests:**
          - RuntimeError with "not initialized" message is raised on upload
        """
        client = S3StorageClient(s3_config)
        with pytest.raises(RuntimeError, match="not initialized"):
            await client.upload("b", "k", b"d", "t")

    @pytest.mark.asyncio
    async def test_stat_returns_object_metadata(
        self, s3_config: S3Config, aws_client: AsyncMock
    ) -> None:
        """Test that stat() HEADs the object and maps ContentLength to size without downloading.

        **Why this test is important:**
          - The ingestion size gate must learn an object's real size WITHOUT downloading it
            (a ``size_bytes==0`` event otherwise materializes the whole object
            in memory before rejection).

        **What it tests:**
          - stat() calls head_object once and returns a StorageObject whose size is ContentLength.
        """
        mock_client = aws_client
        mock_client.head_object = AsyncMock(
            return_value={
                "ContentLength": 4096,
                "ContentType": "application/pdf",
                "LastModified": "2024-01-01",
                "ETag": '"e"',
            }
        )
        client = await _open(s3_config)

        obj = await client.stat("bucket", "big.pdf")
        mock_client.head_object.assert_called_once_with(Bucket="bucket", Key="big.pdf")
        assert obj.size == 4096
        assert obj.content_type == "application/pdf"
        assert obj.key == "big.pdf"

    @pytest.mark.asyncio
    async def test_copy_uses_multipart_for_a_large_object(
        self, s3_config: S3Config, aws_client: AsyncMock
    ) -> None:
        """Test that copy() uses multipart UploadPartCopy for an object over the threshold.

        **Why this test is important:**
          - A single CopyObject of a multi-GB object is slow + timeout-prone (AWS requires multipart
            above 5 GiB); the large-lane relocation must use multipart so a near-cap raw parent copies
            reliably rather than flapping on a copy timeout.

        **What it tests:**
          - With a source larger than the multipart threshold, copy() drives create → per-part
            upload_part_copy → complete and never calls the single copy_object.
        """
        mock_client = aws_client
        size = 6 * 1024**3  # above S3's 5 GiB single-copy limit, so multipart is required
        mock_client.head_object = AsyncMock(return_value={"ContentLength": size})
        mock_client.create_multipart_upload = AsyncMock(return_value={"UploadId": "u1"})
        mock_client.upload_part_copy = AsyncMock(
            return_value={"CopyPartResult": {"ETag": '"e"'}}
        )
        mock_client.complete_multipart_upload = AsyncMock()
        client = await _open(s3_config)

        await client.copy("bucket", "src", "dst")

        mock_client.copy_object.assert_not_called()
        assert mock_client.upload_part_copy.await_count >= 2
        mock_client.complete_multipart_upload.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_multipart_copy_aborts_when_a_part_has_no_etag(
        self, s3_config: S3Config, aws_client: AsyncMock
    ) -> None:
        """Test that a copied part with no ETag aborts the multipart copy with a coded error.

        **Why this test is important:**
          - CompleteMultipartUpload needs every part's ETag; sending an empty one fails
            later with an opaque S3 error, or leaves the upload open and billed.

        **What it tests:**
          - copy() raises AppRuntimeError naming the missing ETag.
          - The upload is aborted and never completed.
        """
        mock_client = aws_client
        mock_client.head_object = AsyncMock(return_value={"ContentLength": 6 * 1024**3})
        mock_client.create_multipart_upload = AsyncMock(return_value={"UploadId": "u1"})
        mock_client.upload_part_copy = AsyncMock(return_value={"CopyPartResult": {}})
        mock_client.complete_multipart_upload = AsyncMock()
        mock_client.abort_multipart_upload = AsyncMock()
        client = await _open(s3_config)

        with pytest.raises(AppRuntimeError, match="ETag"):
            await client.copy("bucket", "src", "dst")

        mock_client.complete_multipart_upload.assert_not_awaited()
        mock_client.abort_multipart_upload.assert_awaited_once_with(
            Bucket="bucket", Key="dst", UploadId="u1"
        )

    @pytest.mark.asyncio
    async def test_list_objects_rejects_an_entry_without_a_key(
        self, s3_config: S3Config, aws_client: AsyncMock
    ) -> None:
        """Test that a listed entry with no Key raises a coded error instead of an empty key.

        **Why this test is important:**
          - An object returned with key "" would be read, copied or deleted at the wrong
            path by the caller; a malformed listing must fail loudly.

        **What it tests:**
          - list_objects raises AppRuntimeError naming the missing Key.
        """
        aws_client.list_objects_v2 = AsyncMock(
            return_value={"Contents": [{"Size": 1, "ETag": "e1"}]}
        )
        client = await _open(s3_config)

        with pytest.raises(AppRuntimeError, match="Key"):
            await client.list_objects("bucket", "prefix/")
