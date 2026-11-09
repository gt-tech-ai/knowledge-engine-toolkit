"""Integration tests for the ``s3db`` checkpoint store against real MinIO.

The object store is real; the stage-state client (another service's API) is a mock of its
consumer-side protocol.
"""

from __future__ import annotations

from typing import TYPE_CHECKING
from unittest.mock import AsyncMock

import pytest
import pytest_asyncio

from techai_webutils.clients.checkpoint import (
    CheckpointConfig,
    CheckpointKind,
    checkpoint_store_from_config,
)
from techai_webutils.clients.storage.s3.s3_client import S3StorageClient
from techai_webutils.core.interfaces.checkpoint import CheckpointKey, Stage, StageArtifact

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.clients.storage.config import S3Config
    from techai_webutils.core.interfaces.checkpoint import CheckpointStore

_FP = "0123456789abcdef"


def _key(
    document_id: str, org_id: str = "org-1", stage: Stage = Stage.PARSE
) -> CheckpointKey:
    """Return the key of ``stage`` for a document whose bytes hash to ``c0ffee``."""
    return CheckpointKey(org_id, document_id, "c0ffee", stage, _FP)


@pytest_asyncio.fixture
async def storage(s3_config: S3Config) -> AsyncIterator[S3StorageClient]:
    """Yield an open S3 client on the test's fresh MinIO bucket."""
    async with S3StorageClient(s3_config) as client:
        yield client


def _store(
    storage: S3StorageClient, bucket: str, max_bytes: int = 1024
) -> CheckpointStore:
    """Build the s3db store over real MinIO with a mocked state client."""
    config = CheckpointConfig(
        kind=CheckpointKind.S3DB, bucket=bucket, max_artifact_bytes=max_bytes
    )
    return checkpoint_store_from_config(
        config, object_store=storage, state_client=AsyncMock()
    )


async def _keys(storage: S3StorageClient, bucket: str) -> list[str]:
    """Return every object key in ``bucket``."""
    return [o.key for o in await storage.list_objects(bucket, "")]


@pytest.mark.integration
@pytest.mark.asyncio
async def test_round_trip_parts_and_auto_segments(
    storage: S3StorageClient, s3_config: S3Config
) -> None:
    """An artifact, two parts, and an over-cap artifact round-trip through MinIO.

    **Why this test is important:**
      - Mocks only assume the key layout and gzip envelope survive a real object store;
        segments must reassemble byte for byte.

    **What it tests:**
      - A small artifact and parts 1 and 2 read back equal; a 5,000-byte artifact over a
        1,024-byte cap is stored as five segments and reads back whole.
    """
    store = _store(storage, s3_config.bucket)
    small = StageArtifact(_key("doc-1"), b'{"text":"hello"}', versions={"parser": "2"})
    big = StageArtifact(_key("doc-1", stage=Stage.CHUNK), bytes(range(250)) * 20)

    await store.put(small)
    await store.put_part(small.key, 1, b"page-1")
    await store.put_part(small.key, 2, b"page-2")
    await store.put(big)

    assert await store.get(small.key) == small
    assert await store.parts(small.key) == frozenset({1, 2})
    assert await store.get_part(small.key, 2) == b"page-2"
    got = await store.get(big.key)
    assert got is not None
    assert got.payload == big.payload
    assert sum(".seg-" in k for k in await _keys(storage, s3_config.bucket)) == 5


@pytest.mark.integration
@pytest.mark.asyncio
async def test_shared_artifact_survives_purge_of_one_referrer(
    storage: S3StorageClient, s3_config: S3Config
) -> None:
    """Two documents with equal bytes share one artifact until both are purged.

    **Why this test is important:**
      - Deleting one copy of a duplicated file must not force the other to re-parse, and
        deleting both must leave nothing behind.

    **What it tests:**
      - After both documents put the same artifact, purging one keeps it readable for the
        other; purging the second leaves the bucket empty.
    """
    store = _store(storage, s3_config.bucket)
    await store.put(StageArtifact(_key("doc-1"), b"shared"))
    await store.put(StageArtifact(_key("doc-2"), b"shared"))

    await store.purge("org-1", "doc-1")
    kept = await store.get(_key("doc-2"))
    await store.purge("org-1", "doc-2")

    assert kept is not None
    assert kept.payload == b"shared"
    assert await _keys(storage, s3_config.bucket) == []


@pytest.mark.integration
@pytest.mark.asyncio
async def test_artifacts_never_shared_across_orgs(
    storage: S3StorageClient, s3_config: S3Config
) -> None:
    """Equal bytes in two orgs are stored twice, and purging one org leaves the other's.

    **Why this test is important:**
      - Sharing across orgs would let one tenant's deletion, or its derived content, reach
        another tenant's documents.

    **What it tests:**
      - Two orgs' equal artifacts live under separate keys; purging org-1's document
        leaves org-2's artifact readable and org-1's gone.
    """
    store = _store(storage, s3_config.bucket)
    await store.put(StageArtifact(_key("doc-1", org_id="org-1"), b"same"))
    await store.put(StageArtifact(_key("doc-1", org_id="org-2"), b"same"))

    await store.purge("org-1", "doc-1")

    assert await store.get(_key("doc-1", org_id="org-1")) is None
    assert await store.get(_key("doc-1", org_id="org-2")) is not None
    assert all("/org-1/" not in k for k in await _keys(storage, s3_config.bucket))
