"""S3-compatible storage client using aiobotocore."""

from __future__ import annotations

import asyncio
import os
from concurrent.futures import ThreadPoolExecutor
from typing import TYPE_CHECKING, Self, cast, final, override

import aiobotocore.session

from techai_webutils.core.errors import AppRuntimeError
from techai_webutils.core.interfaces.storage import StorageClient, StorageObject
from techai_webutils.foundation.logger import get_logger

_DOWNLOAD_CHUNK_SIZE = 1024 * 1024
"""Read size for streamed downloads, in bytes (1 MiB) — keeps peak RAM O(chunk), not
O(file)."""

WRITE_BUFFER_BYTES = 8 * 1024 * 1024
"""Write buffer for streamed downloads, in bytes (8 MiB): eight 1 MiB reads per write."""

_MULTIPART_COPY_THRESHOLD = 100 * 1024 * 1024
"""Object size, in bytes, above which a server-side copy switches to multipart
UploadPartCopy (100 MiB)."""

_COPY_PART_SIZE = 100 * 1024 * 1024
"""Byte range each UploadPartCopy part covers (100 MiB; above the 5 MiB S3 minimum)."""

if TYPE_CHECKING:
    from types import TracebackType

    from aiobotocore.response import StreamingBody
    from aiobotocore.session import AioSession
    from types_aiobotocore_s3 import S3Client
    from types_aiobotocore_s3.type_defs import CompletedPartTypeDef, CopySourceTypeDef

    from techai_webutils.clients.storage.config import S3Config

logger = get_logger(__name__)


def _is_missing_key(exc: Exception) -> bool:
    """Report whether a head_object failure means the key does not exist.

    Returns:
        True for a 404 response.

    """
    response = cast("dict[str, dict[str, str]]", getattr(exc, "response", {}))
    return response.get("Error", {}).get("Code") in {"404", "NoSuchKey"}


def _create_for_write(path: str) -> int:
    """Open ``path`` for writing.

    (created or truncated, mode 0o644) and return its descriptor.

    Returns:
        The open file descriptor.

    """
    return os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)


def _write_all(fd: int, data: bytearray) -> None:
    """Write all of ``data`` to ``fd`` (``os.write`` may write less than it is given)."""
    view = memoryview(data)
    while view:
        view = view[os.write(fd, view) :]


def _required(value: str | None, field: str) -> str:
    """Return a response field S3 always sets but the SDK types mark optional.

    A missing or empty value means a malformed response, so it raises a coded
    ``AppRuntimeError`` naming the field instead of passing an empty string on.

    Returns:
        A response field S3 always sets but the SDK types mark optional.

    Raises:
        AppRuntimeError: S3 response is missing ``field``.

    """
    if not value:
        msg = f"S3 response is missing {field}"
        raise AppRuntimeError(msg)
    return value


@final
class S3StorageClient(StorageClient):
    """Object storage operations backed by S3 via aiobotocore.

    Usage::

        async with S3StorageClient(config) as storage:
            await storage.upload("bucket", "key", b"data", "text/plain")
    """

    def __init__(self, config: S3Config) -> None:
        """Store the S3 connection config; the client is created on context entry."""
        self._config = config
        self._client: S3Client | None = None
        self._session: AioSession | None = None

    @override
    async def __aenter__(self) -> Self:
        """Open the underlying aiobotocore S3 client and return self.

        Returns:
            The underlying aiobotocore S3 client and return self.

        """
        self._session = aiobotocore.session.get_session()
        self._client = await self._session.create_client(
            "s3",
            **self._config.client_kwargs(),
        ).__aenter__()
        return self

    @override
    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Close the underlying aiobotocore S3 client on context exit."""
        if self._client is not None:
            await self._client.__aexit__(exc_type, exc_val, exc_tb)

    @override
    async def upload(self, bucket: str, key: str, body: bytes, content_type: str) -> None:
        """Upload an object to S3.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        _ = await self._client.put_object(
            Bucket=bucket,
            Key=key,
            Body=body,
            ContentType=content_type,
        )

    @override
    async def download(self, bucket: str, key: str) -> bytes:
        """Download an object from S3.

        Returns:
            The object's bytes.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        resp = await self._client.get_object(Bucket=bucket, Key=key)
        async with resp["Body"] as stream:
            return await stream.read()

    @override
    async def delete(self, bucket: str, key: str) -> None:
        """Delete an object from S3.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        _ = await self._client.delete_object(Bucket=bucket, Key=key)

    @override
    async def copy(self, bucket: str, src_key: str, dst_key: str) -> None:
        """Copy an object within the bucket server-side (no data through this process).

        A small object is copied with a single ``CopyObject``; a large one (over
        ``_MULTIPART_COPY_THRESHOLD``) uses multipart ``UploadPartCopy`` — AWS requires it
        above 5 GiB and it is far more reliable than a single multi-GB copy (which is slow
        + timeout-prone). The source size drives the choice; a failed multipart copy
        aborts the upload so no partial object is left behind.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        head = await self._client.head_object(Bucket=bucket, Key=src_key)
        size = int(head["ContentLength"])
        source: CopySourceTypeDef = {"Bucket": bucket, "Key": src_key}
        if size <= _MULTIPART_COPY_THRESHOLD:
            _ = await self._client.copy_object(
                Bucket=bucket, CopySource=source, Key=dst_key
            )
            return
        await self._multipart_copy(bucket, source, dst_key, size)

    async def _multipart_copy(
        self, bucket: str, source: CopySourceTypeDef, dst_key: str, size: int
    ) -> None:
        """Server-side copy a large object via multipart UploadPartCopy.

        Aborting on any failure.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        created = await self._client.create_multipart_upload(Bucket=bucket, Key=dst_key)
        upload_id = created["UploadId"]
        try:
            parts: list[CompletedPartTypeDef] = []
            for part_number, start in enumerate(range(0, size, _COPY_PART_SIZE), start=1):
                end = min(start + _COPY_PART_SIZE, size) - 1
                part = await self._client.upload_part_copy(
                    Bucket=bucket,
                    Key=dst_key,
                    UploadId=upload_id,
                    PartNumber=part_number,
                    CopySource=source,
                    CopySourceRange=f"bytes={start}-{end}",
                )
                parts.append({
                    "ETag": _required(part["CopyPartResult"].get("ETag"), "ETag"),
                    "PartNumber": part_number,
                })
            _ = await self._client.complete_multipart_upload(
                Bucket=bucket,
                Key=dst_key,
                UploadId=upload_id,
                MultipartUpload={"Parts": parts},
            )
        except Exception:
            _ = await self._client.abort_multipart_upload(
                Bucket=bucket, Key=dst_key, UploadId=upload_id
            )
            raise

    @override
    async def exists(self, bucket: str, key: str) -> bool:
        """Check if an object exists in S3.

        Returns:
            Whether the object exists.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        try:
            _ = await self._client.head_object(Bucket=bucket, Key=key)
        except Exception as exc:
            if not _is_missing_key(exc):
                logger.exception("s3 head_object failed; reporting the key as absent")
            return False
        else:
            return True

    @override
    async def presign_url(self, bucket: str, key: str, expiry_seconds: int) -> str:
        """Generate a pre-signed URL for temporary access.

        Returns:
            The signed URL.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        url: str = await self._client.generate_presigned_url(
            "get_object",
            Params={"Bucket": bucket, "Key": key},
            ExpiresIn=expiry_seconds,
        )
        return url

    @override
    async def list_objects(self, bucket: str, prefix: str) -> list[StorageObject]:
        """List objects in a bucket with the given prefix.

        Returns:
            The matching objects.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        resp = await self._client.list_objects_v2(Bucket=bucket, Prefix=prefix)
        objects: list[StorageObject] = [
            StorageObject(
                key=_required(item.get("Key"), "Key"),
                size=item.get("Size", 0),
                content_type="",
                last_modified=str(item.get("LastModified", "")),
                etag=item.get("ETag", ""),
            )
            for item in resp.get("Contents", [])
        ]
        return objects

    @override
    async def stat(self, bucket: str, key: str) -> StorageObject:
        """HEAD an object and map its metadata; ``size`` is the real ``ContentLength``.

        Returns:
            The object's metadata.

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        resp = await self._client.head_object(Bucket=bucket, Key=key)
        return StorageObject(
            key=key,
            size=int(resp["ContentLength"]),
            content_type=resp.get("ContentType", ""),
            last_modified=str(resp.get("LastModified", "")),
            etag=resp.get("ETag", ""),
        )

    @override
    async def download_to_path(self, bucket: str, key: str, path: str) -> None:
        """Stream an object's body to ``path`` chunk-by-chunk.

        (never materialized as one bytes).

        Raises:
            AppRuntimeError: S3StorageClient not initialized. Use as async context
                manager.

        """
        if self._client is None:
            msg = "S3StorageClient not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        resp = await self._client.get_object(Bucket=bucket, Key=key)
        # One dedicated writer thread owns the file for the whole download: the blocking
        # open, writes and close never touch the event loop or the shared default pool
        # (which the parse path also waits on). Chunks collect in an 8 MiB buffer, so
        # eight 1 MiB reads become one write.
        loop = asyncio.get_running_loop()
        writer = ThreadPoolExecutor(max_workers=1, thread_name_prefix="s3-download")
        try:
            fd = await loop.run_in_executor(writer, _create_for_write, path)
            try:
                await self._stream_body(resp["Body"], fd, loop, writer)
            finally:
                await loop.run_in_executor(writer, os.close, fd)
        finally:
            writer.shutdown(wait=False)

    @staticmethod
    async def _stream_body(
        body: StreamingBody,
        fd: int,
        loop: asyncio.AbstractEventLoop,
        writer: ThreadPoolExecutor,
    ) -> None:
        """Copy ``body`` into ``fd`` through a ``WRITE_BUFFER_BYTES`` buffer.

        Each write on ``writer``.
        """
        # aiobotocore's StreamingBody is a wrapt.ObjectProxy over aiohttp's
        # ClientResponse. Its ``__aenter__`` returns the *wrapped* ClientResponse (whose
        # read() takes no size arg), so we must read off the StreamingBody proxy itself —
        # it exposes read(amt) (validating content length), the async analogue of
        # botocore's sync read; the proxy has no iter_chunks(). Keep the proxy reference
        # and use ``async with`` only to close the underlying response on the way out.
        buffer = bytearray()
        async with body:
            while True:
                chunk = await body.read(_DOWNLOAD_CHUNK_SIZE)
                if not chunk:
                    break
                buffer += chunk
                if len(buffer) >= WRITE_BUFFER_BYTES:
                    pending, buffer = buffer, bytearray()
                    await loop.run_in_executor(writer, _write_all, fd, pending)
        if buffer:
            await loop.run_in_executor(writer, _write_all, fd, buffer)
