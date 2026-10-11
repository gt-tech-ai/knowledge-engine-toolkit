"""Storage client builder — ``new_storage_from_config`` (shape; mirrors Go ``storage.NewFromConfig``).

The tier-root factory: selects an object-storage backend by ``StorageKind`` — S3-compatible (AWS
S3 or MinIO, via the ``s3/`` backend) or an in-memory stub (``memory/``) that needs no
infra — and returns the ``StorageClient`` interface. The concrete backend is imported lazily so
selecting a different kind never loads aiobotocore. Unknown kinds fail loudly, matching the Go
``NewFromConfig`` contract.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.storage.config import S3Config
    from techai_webutils.core.interfaces.storage import StorageClient


class StorageKind(StrEnum):
    """Which object-storage backend to build."""

    S3 = "s3"
    """S3-compatible object storage (AWS S3 in cloud, MinIO in dev)."""
    MEMORY = "memory"
    """In-memory stub (dev/test/all-stubs; no external storage)."""


@dataclass(frozen=True, slots=True)
class StorageConfig:
    """Object-storage configuration: the selected backend + its settings."""

    s3: S3Config
    """S3-compatible backend settings (endpoint, bucket, region, credentials)."""
    kind: StorageKind = StorageKind.S3
    """Selects the backend. The zero value is S3."""


def new_storage_from_config(config: StorageConfig) -> StorageClient:
    """Build the ``StorageClient`` selected by ``config.kind`` (heavy backend imported lazily)."""
    if config.kind is StorageKind.S3:
        # Lazy import: skip aiobotocore until selected.
        from techai_webutils.clients.storage.s3.s3_client import S3StorageClient  # noqa: PLC0415

        return S3StorageClient(config.s3)
    if config.kind is StorageKind.MEMORY:
        # Lazy import (and no SDK to load).
        from techai_webutils.clients.storage.memory import InMemoryStorageClient  # noqa: PLC0415

        return InMemoryStorageClient()
    msg = f"unknown storage kind: {config.kind!r}"
    raise AppValueError(msg)
