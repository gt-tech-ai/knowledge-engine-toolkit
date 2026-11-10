"""``NoopSink``: accepts every write, update and delete and stores nothing."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.sink import Sink
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument


class NoopSink(NoOpAsyncResource, Sink):
    """A sink with no backend (dev and tests, and the default until a sink is chosen)."""

    @override
    async def write(
        self,
        document: SinkDocument,
        chunks: Sequence[ChunkPayload],
        vectors: Sequence[Sequence[float]] | None,
    ) -> None:
        """Do nothing.

        Args:
            document: The document the chunks belong to.
            chunks: The chunks, in document order.
            vectors: One embedding per chunk, in order; ``None`` for a sink that does not
                store vectors.

        """

    @override
    async def delete(self, org_id: str, document_id: str) -> None:
        """Do nothing.

        Args:
            org_id: The tenant that owns the document.
            document_id: Ignored.

        """

    @override
    async def update_metadata(
        self, org_id: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Do nothing.

        Args:
            org_id: The tenant that owns the document.
            document_id: Ignored.
            fields: The metadata fields to set.

        """
