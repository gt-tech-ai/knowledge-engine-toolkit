"""Qdrant-backed VectorStore adapter (local-dev vector search).

Wraps ``qdrant_client.AsyncQdrantClient`` behind
``core.interfaces.vector_store.VectorStore``. Collections are created lazily on first
upsert (cosine distance, the configured dimension); the per-point ``payload`` carries
``document_id`` + ``content`` + the entry metadata so a search hit can be reconstructed
into a ``VectorSearchResult`` without a second lookup.
"""

from __future__ import annotations

import asyncio
import uuid
from typing import TYPE_CHECKING, cast, final, override

from qdrant_client import models

from techai_webutils.clients.vector.qdrant.filters import qdrant_filter
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.vector_store import (
    LIST_SEPARATOR,
    VectorEntry,
    VectorSearchResult,
    VectorStore,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.resilience.classify import is_transient

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from qdrant_client import AsyncQdrantClient

    from techai_webutils.core.interfaces.retrieval import FilterExpr
    from techai_webutils.core.interfaces.vector_store import MetadataValue

# Qdrant point ids must be an unsigned int or a UUID; VectorEntry.id is an arbitrary
# string (e.g. "doc-0000.txt:3"). Map each id deterministically to a UUID so upserts stay
# idempotent and the original semantic id is recoverable — the raw id is also stored in
# the payload as "entry_id".
_POINT_ID_NAMESPACE = uuid.NAMESPACE_URL
"""UUID namespace for deriving a deterministic Qdrant point id from an arbitrary
VectorEntry id."""


@final
class QdrantVectorStore(NoOpAsyncResource, VectorStore):
    """VectorStore over Qdrant — lazy cosine collections, payload-carried metadata."""

    def __init__(self, client: AsyncQdrantClient, dimension: int) -> None:
        """Bind the ``AsyncQdrantClient`` and the collection vector ``dimension``.

        The dimension is for collections this store creates.

        Args:
            client: The Qdrant client.
            dimension: The vector size.

        """
        self._client = client
        self._dimension = dimension
        # Collections we have already ensured this process — avoids a round-trip per
        # upsert.
        self._ensured: set[str] = set()
        # Serializes first-time collection creation: concurrent upserts (a batch indexer
        # fanning out) could otherwise both pass the exists-check and race
        # create_collection, the loser erroring "already exists" and failing an
        # otherwise-good document.
        self._ensure_lock = asyncio.Lock()

    @property
    @override
    def dimension(self) -> int:
        """The vector dimension of collections this store creates.

        The single source of truth.
        """
        return self._dimension

    async def _ensure_collection(self, collection: str) -> None:
        """Create ``collection`` (cosine, configured dimension) if it does not exist.

        Double-checked under a lock so concurrent first-time upserts don't race
        create_collection.

        Args:
            collection: The collection.

        Raises:
            AppError: The create failed and the collection is still absent:
                ``UNAVAILABLE`` for a transient failure, else ``INTERNAL``, with the SDK
                error as its cause.

        """
        if collection in self._ensured:
            return
        async with self._ensure_lock:
            # Re-check inside the lock: another coroutine may have created it while we
            # waited.
            if collection in self._ensured:
                return
            if not await self._client.collection_exists(collection):
                try:
                    _ = await self._client.create_collection(
                        collection,
                        vectors_config=models.VectorParams(
                            size=self._dimension, distance=models.Distance.COSINE
                        ),
                    )
                except Exception as exc:
                    # The asyncio lock only serializes THIS process; several processes
                    # (e.g. multiple indexing workers) can still race create_collection on
                    # a first-time upsert — the loser gets a 409 "already exists".
                    # Re-check: if the collection now exists another process won the race
                    # and we are done (idempotent create); otherwise the failure is real,
                    # coded here at the client boundary.
                    if not await self._client.collection_exists(collection):
                        code = (
                            ErrorCode.UNAVAILABLE
                            if is_transient(exc)
                            else ErrorCode.INTERNAL
                        )
                        msg = f"qdrant could not create collection {collection}"
                        raise AppError(code, msg, cause=exc) from exc
            self._ensured.add(collection)

    async def _exists(self, collection: str) -> bool:
        """Whether ``collection`` exists.

        One this process ensured is known without a round-trip.

        Args:
            collection: The collection.

        Returns:
            Whether ``collection`` exists.

        """
        return collection in self._ensured or await self._client.collection_exists(
            collection
        )

    @override
    async def upsert(self, collection: str, entries: Sequence[VectorEntry]) -> None:
        """Ensure the collection, then upsert each entry as a point.

        Each point carries its metadata payload.

        Args:
            collection: The collection.
            entries: The entries to write; an existing id is overwritten.

        """
        await self._ensure_collection(collection)
        points = [
            models.PointStruct(
                id=_point_id(entry.id),
                vector=entry.embedding,
                payload={
                    "entry_id": entry.id,
                    "document_id": entry.document_id,
                    "content": entry.content,
                    **_payload_values(entry.metadata),
                },
            )
            for entry in entries
        ]
        _ = await self._client.upsert(collection, points=points)

    @override
    async def search(
        self,
        collection: str,
        query_embedding: Sequence[float],
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        where: FilterExpr | None = None,
    ) -> list[VectorSearchResult]:
        """Vector-search ``collection``.

        ``filters`` become exact-match payload conditions (e.g. a tenant) and ``where``
        adds an expression (``in`` matches a list payload on any element); both must
        hold.

        Args:
            collection: The collection.
            query_embedding: The query's vector.
            top_k: The most results to return.
            filters: Metadata every result must match; ``None`` matches all.
            where: A further filter every result must match; ``None`` adds none.

        Returns:
            The hits, best first; an ``in`` in ``where`` with no values raises
            ``AppValueError`` (``INVALID_INPUT``).

        """
        query_filter = qdrant_filter(filters, where)
        response = await self._client.query_points(
            collection,
            query=list(query_embedding),
            limit=top_k,
            query_filter=query_filter,
            with_payload=True,
        )
        return [_to_result(point) for point in response.points]

    @override
    async def delete(self, collection: str, ids: Sequence[str]) -> None:
        """Delete points by id (mapped to their deterministic Qdrant point ids).

        Args:
            collection: The collection.
            ids: The entry ids to delete.

        """
        _ = await self._client.delete(
            collection, points_selector=[_point_id(i) for i in ids]
        )

    @override
    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete every point belonging to ``document_id``.

        A payload-filter delete, not id-based.

        Args:
            collection: The collection.
            document_id: The document whose entries are deleted.

        """
        _ = await self._client.delete(
            collection,
            points_selector=models.FilterSelector(filter=_document_filter(document_id)),
        )

    @override
    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Count the points belonging to ``document_id``.

        0 when the collection does not exist yet.

        Args:
            collection: The collection.
            document_id: The document.

        Returns:
            The points belonging to ``document_id``.

        """
        if not await self._exists(collection):
            return 0
        result = await self._client.count(
            collection, count_filter=_document_filter(document_id), exact=True
        )
        return result.count

    @override
    async def stored_metadata(
        self, collection: str, ids: Sequence[str]
    ) -> dict[str, dict[str, str]]:
        """Return ``{raw id: stored metadata}`` for the ``ids`` already stored.

        A checkpoint/resume probe.

        Maps each raw id to its deterministic Qdrant point id and retrieves those points
        with their payload (no vectors), reporting each present point's payload minus the
        keys promoted to entry fields; empty when the collection does not exist yet. Lets
        the incremental indexer skip the sub-batches already upserted with the same
        metadata on a redrive instead of re-embedding them (the expensive CPU step), while
        a chunk stored with other metadata is rewritten.

        Args:
            collection: The collection.
            ids: The entry ids to look up.

        Returns:
            ``{raw id: stored metadata}`` for the ``ids`` already stored.

        """
        if not ids or not await self._exists(collection):
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
    """Map an arbitrary VectorEntry id to a deterministic Qdrant point id (a UUID).

    Args:
        raw_id: The entry's id.

    Returns:
        The deterministic point id.

    """
    return str(uuid.uuid5(_POINT_ID_NAMESPACE, raw_id))


def _document_filter(document_id: str) -> models.Filter:
    """Build a payload filter for points whose ``document_id`` matches.

    It matches ``document_id`` exactly.

    Args:
        document_id: The document.

    Returns:
        A payload filter matching every point whose ``document_id`` equals.

    """
    return models.Filter(
        must=[
            models.FieldCondition(
                key="document_id", match=models.MatchValue(value=document_id)
            )
        ],
    )


# Payload keys promoted to top-level VectorSearchResult fields — excluded from the
# metadata dict so they are not duplicated there.
_PROMOTED_PAYLOAD_KEYS = frozenset({"content", "document_id", "entry_id"})
"""Payload keys promoted to top-level VectorSearchResult fields (excluded from the
metadata dict)."""


def _payload_values(metadata: Mapping[str, MetadataValue]) -> dict[str, object]:
    """Return ``metadata`` as payload values: a tuple becomes a list.

    Qdrant matches a list payload on any element, which is what ``in`` asks for.

    Args:
        metadata: The entry's metadata.

    Returns:
        The payload values.

    """
    return {k: v if isinstance(v, str) else list(v) for k, v in metadata.items()}


def _metadata(payload: Mapping[str, object]) -> dict[str, str]:
    """Return a point payload's metadata.

    Every key not promoted to an entry field, stringified; a list joined with
    ``LIST_SEPARATOR``.

    Args:
        payload: The point's payload.

    Returns:
        A point payload's metadata: every key not promoted to an entry field.

    """
    return {
        key: (
            LIST_SEPARATOR.join(str(v) for v in cast("list[object]", value))
            if isinstance(value, list)
            else str(value)
        )
        for key, value in payload.items()
        if key not in _PROMOTED_PAYLOAD_KEYS
    }


def _to_result(point: models.ScoredPoint) -> VectorSearchResult:
    """Map a Qdrant hit into a VectorSearchResult.

    Payload keys not promoted to fields become metadata.

    Args:
        point: The scored point.

    Returns:
        The mapped result.

    """
    payload = cast("dict[str, object]", point.payload or {})
    return VectorSearchResult(
        document_id=str(payload.get("document_id", "")),
        # The original semantic id (e.g. "doc-0000.txt:3") is stored in the payload as
        # "entry_id"; point.id is the opaque deterministic UUID, used only as a fallback
        # if the payload lacks it.
        chunk_id=str(payload.get("entry_id", point.id)),
        content=str(payload.get("content", "")),
        score=float(point.score),
        metadata=_metadata(payload),
    )
