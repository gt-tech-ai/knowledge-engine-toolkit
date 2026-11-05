"""``NoopCheckpointStore``: no checkpoints, so every stage runs (the default, today's behaviour)."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.checkpoint import CheckpointStore

if TYPE_CHECKING:
    from collections.abc import Collection

    from techai_webutils.core.interfaces.checkpoint import (
        CheckpointKey,
        Stage,
        StageArtifact,
    )


class NoopCheckpointStore(CheckpointStore):
    """Misses every read and drops every write."""

    @override
    async def get(self, key: CheckpointKey) -> StageArtifact | None:
        """Miss: return ``None``."""
        return None

    @override
    async def put(self, artifact: StageArtifact) -> None:
        """Drop the artifact."""

    @override
    async def put_part(self, key: CheckpointKey, n: int, payload: bytes) -> None:
        """Drop the part."""

    @override
    async def get_part(self, key: CheckpointKey, n: int) -> bytes | None:
        """Miss: return ``None``."""
        return None

    @override
    async def parts(self, key: CheckpointKey) -> frozenset[int]:
        """Return no parts."""
        return frozenset()

    @override
    async def delete(
        self, org_id: str, document_id: str, stages: Collection[Stage]
    ) -> None:
        """Nothing is stored, so nothing is deleted."""

    @override
    async def purge(self, org_id: str, document_id: str) -> None:
        """Nothing is stored, so nothing is purged."""
