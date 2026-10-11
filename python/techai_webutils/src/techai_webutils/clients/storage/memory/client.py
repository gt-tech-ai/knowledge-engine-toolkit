"""In-memory object-storage backend for no-infra builds/tests.

``InMemoryStorageClient`` keeps objects in a process-local map so a service can build and unit-test the
storage path with no S3/MinIO — the stub-first property (ARCHITECTURE.md#stub-first-backends). Mirrors
Go's ``clients/storage/memory``. Selected via ``StorageKind.MEMORY``. ``presign_url`` returns a
synthetic ``memory://`` URL: a memory backend never produces real signed URLs, so it is confined to
test/CI/all-stubs config and is never reachable from a real client-facing upload flow.
"""

from __future__ import annotations

import asyncio
import hashlib
from datetime import UTC, datetime
from pathlib import Path
from typing import override

from techai_webutils.core.errors.errors import NotFoundError
from techai_webutils.core.interfaces.storage import StorageClient, StorageObject
from techai_webutils.foundation.lifecycle import NoOpAsyncResource


class InMemoryStorageClient(NoOpAsyncResource, StorageClient):
    """A process-local ``StorageClient`` backed by a dict (dev/test; no external storage).

    Owns no external resource, so its async-context lifecycle is the shared ``NoOpAsyncResource``.
    """

    def __init__(self) -> None:
        """Start with an empty object store keyed by ``(bucket, key)``."""
        self._objects: dict[tuple[str, str], tuple[bytes, StorageObject]] = {}

    async def upload(self, bucket: str, key: str, body: bytes, content_type: str) -> None:
        """Store ``body`` under ``(bucket, key)`` with its computed metadata."""
        meta = StorageObject(
            key=key,
            size=len(body),
            content_type=content_type,
            last_modified=datetime.now(UTC).isoformat(),
            etag=hashlib.sha256(body).hexdigest(),
        )
        self._objects[bucket, key] = (body, meta)

    async def download(self, bucket: str, key: str) -> bytes:
        """Return the stored bytes, or raise ``NotFoundError`` if the key is absent."""
        return self._require(bucket, key)[0]

    async def delete(self, bucket: str, key: str) -> None:
        """Remove the object; a missing key is a no-op (S3 delete is idempotent)."""
        self._objects.pop((bucket, key), None)

    async def copy(self, bucket: str, src_key: str, dst_key: str) -> None:
        """Copy the object within the bucket, preserving its content type."""
        body, meta = self._require(bucket, src_key)
        await self.upload(bucket, dst_key, body, meta.content_type)

    async def exists(self, bucket: str, key: str) -> bool:
        """Report whether an object exists at ``(bucket, key)``."""
        return (bucket, key) in self._objects

    @override
    async def presign_url(self, bucket: str, key: str, expiry_seconds: int) -> str:
        """Return a synthetic ``memory://`` URL (no real signing; the expiry is ignored)."""
        del expiry_seconds  # a memory backend issues no time-bounded signature
        return f"memory://{bucket}/{key}"

    async def list_objects(self, bucket: str, prefix: str) -> list[StorageObject]:
        """List the metadata of objects in ``bucket`` whose key starts with ``prefix``."""
        return [
            meta
            for (obj_bucket, key), (_body, meta) in self._objects.items()
            if obj_bucket == bucket and key.startswith(prefix)
        ]

    async def stat(self, bucket: str, key: str) -> StorageObject:
        """Return the object's metadata, or raise ``NotFoundError`` if absent."""
        return self._require(bucket, key)[1]

    async def download_to_path(self, bucket: str, key: str, path: str) -> None:
        """Write the stored bytes to the local file ``path`` (off-thread, so the loop is not blocked)."""
        body = self._require(bucket, key)[0]
        await asyncio.to_thread(Path(path).write_bytes, body)

    def _require(self, bucket: str, key: str) -> tuple[bytes, StorageObject]:
        """Return the stored ``(body, meta)`` or raise ``NotFoundError``."""
        try:
            return self._objects[bucket, key]
        except KeyError as exc:
            msg = f"object not found: {bucket}/{key}"
            raise NotFoundError(msg) from exc
