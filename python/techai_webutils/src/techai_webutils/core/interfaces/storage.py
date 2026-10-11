"""Storage client interface for object storage (S3-compatible).

Mirrors Go's ``interfaces.StorageClient`` and ``StorageObject``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass
class StorageObject:
    """Metadata about a stored object."""

    key: str
    """The object's key (path) within its bucket."""
    size: int
    """Object size in bytes."""
    content_type: str
    """The object's stored MIME type."""
    last_modified: str
    """Timestamp of the object's last modification, as reported by the backend."""
    etag: str
    """The backend's entity tag for the object, used for change detection/idempotency."""


class StorageClient(ManagedResource, ABC):
    """Object storage operations (S3-compatible).

    Composes ``ManagedResource`` (ARCHITECTURE.md#interface-composition): a storage client owns an async
    connection lifecycle (``__aenter__``/``__aexit__``), so a composition root manages
    it with an ``AsyncExitStack`` and the ``new_storage_from_config`` factory returns a
    value that is itself an async context manager.
    """

    @abstractmethod
    async def upload(self, bucket: str, key: str, body: bytes, content_type: str) -> None:
        """Store an object with the given key in the specified bucket."""
        ...

    @abstractmethod
    async def download(self, bucket: str, key: str) -> bytes:
        """Retrieve an object by key from the specified bucket."""
        ...

    @abstractmethod
    async def delete(self, bucket: str, key: str) -> None:
        """Remove an object by key from the specified bucket."""
        ...

    @abstractmethod
    async def copy(self, bucket: str, src_key: str, dst_key: str) -> None:
        """Copy an object within the bucket server-side (no bytes through this process)."""
        ...

    @abstractmethod
    async def exists(self, bucket: str, key: str) -> bool:
        """Check if an object exists at the given key."""
        ...

    @abstractmethod
    async def presign_url(self, bucket: str, key: str, expiry_seconds: int) -> str:
        """Generate a pre-signed URL for temporary access to an object."""
        ...

    @abstractmethod
    async def list_objects(self, bucket: str, prefix: str) -> list[StorageObject]:
        """List objects in a bucket with the given prefix."""
        ...

    @abstractmethod
    async def stat(self, bucket: str, key: str) -> StorageObject:
        """Return an object's metadata (notably its real size) WITHOUT downloading the body.

        Lets a caller gate on the true object size before paying to fetch it (the large-document
        streaming path). Raises the backend's not-found error when the object is absent.
        """
        ...

    @abstractmethod
    async def download_to_path(self, bucket: str, key: str, path: str) -> None:
        """Stream an object's body to the local file ``path`` chunk-by-chunk.

        Peak memory is O(chunk), not O(file) — unlike ``download`` which returns the whole object as
        one ``bytes``. Used by the large-document ingestion lane so a multi-GB object never lands in RAM.
        """
        ...
