"""Tests for the in-memory storage backend + its factory selection.

The ``memory`` StorageClient lets a service build and unit-test the object-storage path with no
S3/MinIO — the stub-first property (ARCHITECTURE.md#stub-first-backends). Parity with Go's ``storage/memory``.
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.storage.builder import (
    StorageConfig,
    StorageKind,
    new_storage_from_config,
)
from techai_webutils.clients.storage.config import S3Config
from techai_webutils.clients.storage.memory import InMemoryStorageClient
from techai_webutils.core.errors.errors import NotFoundError


@pytest.mark.asyncio
async def test_storage_memory_backend_roundtrip() -> None:
    """The in-memory client round-trips an object through the full StorageClient surface.

    Why this test is important:
        - The memory backend is only useful as a no-infra stand-in if it behaves like real object
          storage for the operations services actually use (put/get/stat/list/delete); a partial
          impl would let a build pass but break the first real call path.

    What it tests:
        - upload → exists/download/stat/list return the stored object; presign yields a synthetic
          ``memory://`` URL; delete removes it; a missing key raises NotFoundError.
    """
    async with InMemoryStorageClient() as store:
        await store.upload("bucket", "k", b"hello", "text/plain")

        assert await store.exists("bucket", "k") is True
        assert await store.download("bucket", "k") == b"hello"

        obj = await store.stat("bucket", "k")
        assert obj.size == 5
        assert obj.content_type == "text/plain"

        listed = await store.list_objects("bucket", "")
        assert [o.key for o in listed] == ["k"]

        url = await store.presign_url("bucket", "k", 60)
        assert url.startswith("memory://"), "the memory backend returns a synthetic URL"

        await store.delete("bucket", "k")
        assert await store.exists("bucket", "k") is False

        with pytest.raises(NotFoundError):
            await store.download("bucket", "missing")


def test_new_storage_from_config_selects_memory() -> None:
    """The factory returns the in-memory client for ``StorageKind.MEMORY`` (config-selects-impl).

    Why this test is important:
        - The memory backend is opted into by a config kind; if the factory silently returned the
          S3 client, the configured no-infra behavior would never take effect (and would try to
          reach S3).

    What it tests:
        - ``new_storage_from_config`` with ``kind=MEMORY`` returns an ``InMemoryStorageClient``.
    """
    config = StorageConfig(
        s3=S3Config(endpoint="", bucket="bucket", region="us-east-1"),
        kind=StorageKind.MEMORY,
    )
    assert isinstance(new_storage_from_config(config), InMemoryStorageClient)
