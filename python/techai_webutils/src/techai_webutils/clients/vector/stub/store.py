"""In-memory stub vector store for no-infra builds/tests.

``StubVectorStore`` keeps entries in a process-local map and ranks searches by cosine
similarity, so the retrieval path can build and unit-test with no Qdrant. Deterministic
(same vectors ⇒ same ranking). Mirrors the ``qdrant`` sibling's shape behind the
``VectorStore`` interface.
"""

from __future__ import annotations

import math
from operator import itemgetter
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import (
    FilterAnd,
    FilterEq,
    FilterIn,
    equals_all,
)
from techai_webutils.core.interfaces.vector_store import (
    VectorEntry,
    VectorSearchResult,
    VectorStore,
    flat_metadata,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.retrieval import FilterExpr
    from techai_webutils.core.interfaces.vector_store import MetadataValue


def _cosine(a: list[float], b: list[float]) -> float:
    """Return the cosine similarity of two equal-length vectors.

    0.0 when either has zero norm.

    Args:
        a: One vector.
        b: The other.

    Returns:
        The cosine similarity.

    """
    dot = sum(x * y for x, y in zip(a, b, strict=False))
    norm_a = math.sqrt(sum(x * x for x in a))
    norm_b = math.sqrt(sum(y * y for y in b))
    if norm_a == 0 or norm_b == 0:
        return 0.0
    return dot / (norm_a * norm_b)


def _holds(metadata: Mapping[str, MetadataValue], expr: FilterExpr) -> bool:
    """Report whether ``metadata`` satisfies ``expr``, as Qdrant and OpenSearch would.

    ``eq`` and ``in`` match a list value on any element, so a tag list matches either.

    Args:
        metadata: An entry's metadata.
        expr: The filter expression.

    Returns:
        Whether the metadata satisfies the expression.

    Raises:
        AppValueError: An ``in`` with no values (``INVALID_INPUT``).

    """
    if isinstance(expr, FilterEq | FilterIn):
        stored = metadata.get(expr.key)
        held = () if stored is None else (stored,) if isinstance(stored, str) else stored
        wanted = (expr.value,) if isinstance(expr, FilterEq) else expr.values
        if not wanted:
            msg = f"an in filter on {expr.key!r} needs at least one value"
            raise AppValueError(msg)
        return any(value in held for value in wanted)
    return all(_holds(metadata, inner) for inner in expr.clauses)


@final
class StubVectorStore(NoOpAsyncResource, VectorStore):
    """A process-local ``VectorStore`` ranking by cosine similarity (dev/test; no Qdrant).

    Owns no external resource, so its async-context lifecycle is the shared
    ``NoOpAsyncResource``.
    """

    def __init__(self, dimension: int) -> None:
        """Store the collection map and the vector dimension the store reports.

        Args:
            dimension: The vector size.

        """
        self._dimension = dimension
        self._collections: dict[str, dict[str, VectorEntry]] = {}

    @property
    @override
    def dimension(self) -> int:
        """The configured vector dimension."""
        return self._dimension

    @override
    async def upsert(self, collection: str, entries: list[VectorEntry]) -> None:
        """Insert or update ``entries`` in ``collection`` keyed by entry id.

        Args:
            collection: The collection.
            entries: The entries to write; an existing id is overwritten.

        """
        store = self._collections.setdefault(collection, {})
        for entry in entries:
            store[entry.id] = entry

    @override
    async def search(
        self,
        collection: str,
        query_embedding: list[float],
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        where: FilterExpr | None = None,
    ) -> list[VectorSearchResult]:
        """Return the ``top_k`` entries most similar to ``query_embedding``.

        By cosine, among the entries matching ``filters`` and ``where``.

        Args:
            collection: The collection.
            query_embedding: The query's vector.
            top_k: The most results to return.
            filters: Metadata every result must match; ``None`` matches all.
            where: A further filter (``in`` matches a list on any element); ``None``
                adds none.

        Returns:
            The ``top_k`` entries most similar to ``query_embedding`` (cosine).

        """
        narrowing = equals_all(filters or {})
        if where is not None:
            narrowing = FilterAnd((narrowing, where))
        scored = [
            (_cosine(query_embedding, entry.embedding), entry)
            for entry in self._collections.get(collection, {}).values()
            if _holds(entry.metadata, narrowing)
        ]
        scored.sort(key=itemgetter(0), reverse=True)
        return [
            VectorSearchResult(
                document_id=entry.document_id,
                chunk_id=entry.id,
                content=entry.content,
                score=score,
                metadata=flat_metadata(entry.metadata),
            )
            for score, entry in scored[:top_k]
        ]

    @override
    async def delete(self, collection: str, ids: list[str]) -> None:
        """Delete entries by id from ``collection`` (missing ids are ignored).

        Args:
            collection: The collection.
            ids: The entry ids to delete.

        """
        store = self._collections.get(collection, {})
        for entry_id in ids:
            _ = store.pop(entry_id, None)

    @override
    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete every entry belonging to ``document_id`` in ``collection``.

        Args:
            collection: The collection.
            document_id: The document whose entries are deleted.

        """
        store = self._collections.get(collection, {})
        for entry_id in [
            eid for eid, entry in store.items() if entry.document_id == document_id
        ]:
            del store[entry_id]

    @override
    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Count the entries belonging to ``document_id`` in ``collection``.

        Args:
            collection: The collection.
            document_id: The document.

        Returns:
            The entries belonging to ``document_id`` in ``collection``.

        """
        store = self._collections.get(collection, {})
        return sum(1 for entry in store.values() if entry.document_id == document_id)

    @override
    async def stored_metadata(
        self, collection: str, ids: list[str]
    ) -> dict[str, dict[str, str]]:
        """Return ``{id: metadata}`` for the ``ids`` already stored in ``collection``.

        Args:
            collection: The collection.
            ids: The entry ids to look up.

        Returns:
            ``{id: metadata}`` for the ``ids`` already stored in ``collection``, list
            values joined with ``LIST_SEPARATOR`` as every store reads them back.

        """
        store = self._collections.get(collection, {})
        return {
            entry_id: flat_metadata(store[entry_id].metadata)
            for entry_id in ids
            if entry_id in store
        }
