"""Qdrant-backed VectorStore adapter (local-dev vector search).

Wraps ``qdrant_client.AsyncQdrantClient`` behind ``core.interfaces.vector_store.VectorStore``.
Collections are created lazily on first upsert (cosine distance, the configured dimension); the
per-point ``payload`` carries ``document_id`` + ``content`` + the entry metadata so a search hit can
be reconstructed into a ``VectorSearchResult`` without a second lookup.
"""

from __future__ import annotations

import asyncio
import uuid
from typing import TYPE_CHECKING

from qdrant_client import models

from techai_webutils.core.interfaces.vector_store import (
    VectorEntry,
    VectorSearchResult,
    VectorStore,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from qdrant_client import AsyncQdrantClient

# Qdrant point ids must be an unsigned int or a UUID; VectorEntry.id is an arbitrary string (e.g.
# "doc-0000.txt:3"). Map each id deterministically to a UUID so upserts stay idempotent and the
# original semantic id is recoverable — the raw id is also stored in the payload as "entry_id".
_POINT_ID_NAMESPACE = uuid.NAMESPACE_URL
"""UUID namespace for deriving a deterministic Qdrant point id from an arbitrary VectorEntry id."""


class QdrantVectorStore(NoOpAsyncResource, VectorStore):
    """VectorStore over Qdrant — lazy cosine collections, payload-carried metadata."""

    def __init__(self, client: AsyncQdrantClient, dimension: int) -> None:
        """Bind the ``AsyncQdrantClient`` and the collection vector ``dimension`` this store creates."""
        self._client = client
        self._dimension = dimension
        # Collections we have already ensured this process — avoids a round-trip per upsert.
        self._ensured: set[str] = set()
        # Serializes first-time collection creation: concurrent upserts (a batch indexer fanning out)
        # could otherwise both pass the exists-check and race create_collection,
        # the loser erroring "already exists" and failing an otherwise-good document.
        self._ensure_lock = asyncio.Lock()

    @property
    def dimension(self) -> int:
        """The vector dimension of collections this store creates (single source of truth)."""
        return self._dimension

    async def _ensure_collection(self, collection: str) -> None:
        """Create ``collection`` (cosine, configured dimension) if it does not already exist.

        Double-checked under a lock so concurrent first-time upserts don't race create_collection.
        """
        if collection in self._ensured:
            return
        async with self._ensure_lock:
            # Re-check inside the lock: another coroutine may have created it while we waited.
            if collection in self._ensured:
                return
            if not await self._client.collection_exists(collection):
                try:
                    await self._client.create_collection(
                        collection,
                        vectors_config=models.VectorParams(
                            size=self._dimension, distance=models.Distance.COSINE
                        ),
                    )
                except Exception:
                    # The asyncio lock only serializes THIS process; several processes (e.g. multiple
                    # indexing workers) can still race create_collection on a first-time upsert — the
                    # loser gets a 409 "already exists". Re-check: if the collection now exists another
                    # process won the race and we are done (idempotent create); otherwise the failure
                    # is real, so re-raise.
                    if not await self._client.collection_exists(collection):
                        raise
            self._ensured.add(collection)

    async def upsert(self, collection: str, entries: Sequence[VectorEntry]) -> None:
        """Ensure the collection, then upsert each entry as a point carrying its metadata payload."""
        await self._ensure_collection(collection)
        points = [
            models.PointStruct(
                id=_point_id(entry.id),
                vector=list(entry.embedding),
                payload={
                    "entry_id": entry.id,
                    "document_id": entry.document_id,
                    "content": entry.content,
                    **entry.metadata,
                },
            )
            for entry in entries
        ]
        await self._client.upsert(collection, points=points)

    async def search(
        self,
        collection: str,
        query_embedding: Sequence[float],
        top_k: int = 10,
        filters: dict[str, str] | None = None,
    ) -> list[VectorSearchResult]:
        """Vector-search ``collection``; ``filters`` become an exact-match payload filter (e.g. a tenant)."""
        query_filter = None
        if filters:
            query_filter = models.Filter(
                must=[
                    models.FieldCondition(key=key, match=models.MatchValue(value=value))
                    for key, value in filters.items()
                ],
            )
        response = await self._client.query_points(
            collection,
            query=list(query_embedding),
            limit=top_k,
            query_filter=query_filter,
            with_payload=True,
        )
        return [_to_result(point) for point in response.points]

    async def delete(self, collection: str, ids: Sequence[str]) -> None:
        """Delete points by id (mapped to their deterministic Qdrant point ids)."""
        await self._client.delete(collection, points_selector=[_point_id(i) for i in ids])

    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete every point belonging to ``document_id`` (a payload-filter delete, not id-based)."""
        await self._client.delete(
            collection,
            points_selector=models.FilterSelector(filter=_document_filter(document_id)),
        )

    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Count the points belonging to ``document_id``; 0 when the collection does not exist yet."""
        if not await self._client.collection_exists(collection):
            return 0
        result = await self._client.count(
            collection, count_filter=_document_filter(document_id), exact=True
        )
        return result.count

    async def stored_metadata(
        self, collection: str, ids: Sequence[str]
    ) -> dict[str, dict[str, str]]:
        """Return ``{raw id: stored metadata}`` for the ``ids`` already stored (a checkpoint/resume probe).

        Maps each raw id to its deterministic Qdrant point id and retrieves those points with their
        payload (no vectors), reporting each present point's payload minus the keys promoted to entry
        fields; empty when the collection does not exist yet. Lets the incremental indexer skip the
        sub-batches already upserted with the same metadata on a redrive instead of re-embedding them
        (the expensive CPU step), while a chunk stored with other metadata is rewritten.
        """
        if not ids or not await self._client.collection_exists(collection):
            return {}
        by_point_id = {_point_id(raw): raw for raw in ids}
        points = await self._client.retrieve(
            collection,
            ids=list(by_point_id),
            with_payload=True,
            with_vectors=False,
        )
        return {
            by_point_id[str(point.id)]: _metadata(point.payload or {})
            for point in points
            if str(point.id) in by_point_id
        }


def _point_id(raw_id: str) -> str:
    """Map an arbitrary VectorEntry id to a deterministic Qdrant point id (a UUID)."""
    return str(uuid.uuid5(_POINT_ID_NAMESPACE, raw_id))


def _document_filter(document_id: str) -> models.Filter:
    """Build a payload filter matching every point whose ``document_id`` equals ``document_id``."""
    return models.Filter(
        must=[
            models.FieldCondition(
                key="document_id", match=models.MatchValue(value=document_id)
            )
        ],
    )


# Payload keys promoted to top-level VectorSearchResult fields — excluded from the metadata dict so
# they are not duplicated there.
_PROMOTED_PAYLOAD_KEYS = frozenset({"content", "document_id", "entry_id"})
"""Payload keys promoted to top-level VectorSearchResult fields (excluded from the metadata dict)."""


def _metadata(payload: Mapping[str, object]) -> dict[str, str]:
    """Return a point payload's metadata: every key not promoted to an entry field, stringified."""
    return {
        key: str(value)
        for key, value in payload.items()
        if key not in _PROMOTED_PAYLOAD_KEYS
    }


def _to_result(point: models.ScoredPoint) -> VectorSearchResult:
    """Map a Qdrant hit into a VectorSearchResult; payload keys not promoted to fields become metadata."""
    payload = point.payload or {}
    return VectorSearchResult(
        document_id=str(payload.get("document_id", "")),
        # The original semantic id (e.g. "doc-0000.txt:3") is stored in the payload as "entry_id";
        # point.id is the opaque deterministic UUID, used only as a fallback if the payload lacks it.
        chunk_id=str(payload.get("entry_id", point.id)),
        content=str(payload.get("content", "")),
        score=float(point.score),
        metadata=_metadata(payload),
    )
