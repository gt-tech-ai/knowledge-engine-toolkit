"""``NoopCheckpointStore``: no checkpoints, so every stage runs (the default)."""

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
        """Miss: return ``None``.

        Args:
            key: Ignored.

        Returns:
            ``None``, always: this store holds nothing.

        """
        return None

    @override
    async def put(self, artifact: StageArtifact) -> None:
        """Drop the artifact.

        Args:
            artifact: The stage output to store, carrying its own key.

        """

    @override
    async def put_part(self, key: CheckpointKey, n: int, payload: bytes) -> None:
        """Drop the part.

        Args:
            key: The stage output the part belongs to.
            n: The part number, from 0.
            payload: The part's bytes.

        """

    @override
    async def get_part(self, key: CheckpointKey, n: int) -> bytes | None:
        """Miss: return ``None``.

        Args:
            key: The stage output the part belongs to.
            n: The part number, from 0.

        Returns:
            ``None``, always: this store holds nothing.

        """
        return None

    @override
    async def parts(self, key: CheckpointKey) -> frozenset[int]:
        """Return no parts.

        Args:
            key: The stage output whose parts are listed.

        Returns:
            No parts.

        """
        return frozenset()

    @override
    async def delete(
        self, org_id: str, document_id: str, stages: Collection[Stage]
    ) -> None:
        """Nothing is stored, so nothing is deleted.

        Args:
            org_id: The tenant that owns the document.
            document_id: Ignored.
            stages: The stages whose references are dropped.

        """

    @override
    async def purge(self, org_id: str, document_id: str) -> None:
        """Nothing is stored, so nothing is purged.

        Args:
            org_id: The tenant that owns the document.
            document_id: The document whose references are all dropped.

        """
