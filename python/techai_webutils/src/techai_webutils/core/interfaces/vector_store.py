"""Vector store interface for similarity search.

Keyword search too, where the store has one.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING, NoReturn

from techai_webutils.core.errors import AppValueError, InternalError
from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.retrieval import FilterExpr

type MetadataValue = str | tuple[str, ...]
"""One stored metadata value: a string, or a list of strings (e.g. tags) that an ``in``
filter matches on any element."""

LIST_SEPARATOR = "|"
"""Joins a list value's elements when a store reads it back as one metadata string."""


def flat_metadata(metadata: Mapping[str, MetadataValue]) -> dict[str, str]:
    """Return ``metadata`` as a store reads it back: list values joined.

    A list value comes back as its elements joined with ``LIST_SEPARATOR``, so compare
    stored metadata against this form, never against the lists themselves.

    Args:
        metadata: Metadata as written.

    Returns:
        Each value as a string, a list joined with ``LIST_SEPARATOR``.

    """
    return {
        key: value if isinstance(value, str) else LIST_SEPARATOR.join(value)
        for key, value in metadata.items()
    }


def scalar_metadata(metadata: Mapping[str, MetadataValue], key: str) -> str | None:
    """Return ``metadata[key]`` when it is a single string; ``None`` when absent.

    For a key a store routes on (an org, a shard), where a list has no one meaning.

    Args:
        metadata: Metadata as written.
        key: The key.

    Returns:
        The key's string value, or ``None`` when ``metadata`` lacks it.

    Raises:
        AppValueError: The key holds a list (``INVALID_INPUT``).

    """
    value = metadata.get(key)
    if value is None or isinstance(value, str):
        return value
    msg = f"metadata {key!r} must be one string, not a list"
    raise AppValueError(msg)


@dataclass(slots=True)
class VectorSearchResult:
    """A single result from a vector similarity search."""

    document_id: str
    """Identifier of the source document the matched chunk belongs to."""
    chunk_id: str
    """Identifier of the specific chunk that matched the query vector."""
    content: str
    """The matched chunk's text."""
    score: float
    """Similarity score between the query and this chunk (higher is closer)."""
    metadata: dict[str, str]
    """Metadata stored with the chunk (used for filtering and citation display); a list
    value reads back joined with ``LIST_SEPARATOR``."""


@dataclass(slots=True)
class VectorEntry:
    """A vector entry to be stored."""

    id: str
    """Unique identifier for this entry, used for upsert and existence probes."""
    document_id: str
    """Identifier of the source document this entry belongs to."""
    content: str
    """The chunk text stored alongside its vector."""
    embedding: list[float]
    """The dense vector to index (its dimension must match the store's ``dimension``)."""
    metadata: Mapping[str, MetadataValue]
    """Metadata stored with the entry, available for filtered search and citations; a
    tuple value is stored as a list (read back joined, see ``flat_metadata``)."""


class VectorStore(ManagedResource, ABC):
    """Abstract vector store for embedding storage and similarity search.

    Upsert, search and delete.
    """

    @property
    @abstractmethod
    def dimension(self) -> int:
        """The vector dimension of collections this store manages.

        It must match the embedder.
        """

    @abstractmethod
    async def upsert(self, collection: str, entries: list[VectorEntry]) -> None:
        """Insert or update vector entries in the specified collection.

        Args:
            collection: The collection.
            entries: The entries to write; an existing id is overwritten.

        """

    @abstractmethod
    async def search(
        self,
        collection: str,
        query_embedding: list[float],
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        where: FilterExpr | None = None,
    ) -> list[VectorSearchResult]:
        """Search for similar vectors in the specified collection.

        ``filters`` and ``where`` both narrow the search: a result must equal every
        ``filters`` entry and satisfy ``where``. An ``in`` in ``where`` matches a
        list-valued field when any element is one of its values; an ``in`` with no
        values is ``INVALID_INPUT``.

        Args:
            collection: The collection.
            query_embedding: The query's vector.
            top_k: The most results to return.
            filters: Metadata every result must match; ``None`` matches all.
            where: A further filter expression; ``None`` adds none.

        Returns:
            The nearest entries with their scores, best first.

        """

    @property
    def supports_keyword(self) -> bool:
        """Whether ``keyword_search`` is available.

        A BM25 index beside the vectors; ``False`` by default.
        """
        return False

    async def keyword_search(
        self,
        collection: str,
        query: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        where: FilterExpr | None = None,
    ) -> list[VectorSearchResult]:
        """Search the stored text lexically (BM25).

        Only stores with ``supports_keyword`` implement it; this default raises
        ``InternalError``, so callers check ``supports_keyword`` first. ``filters`` and
        ``where`` narrow it as they do ``search``.

        Args:
            collection: The collection.
            query: The query text.
            top_k: The most results to return.
            filters: Metadata every result must match; ``None`` matches all.
            where: A further filter expression; ``None`` adds none.

        Returns:
            The best lexical matches with their BM25 scores, best first.

        """
        return _no_keyword_search(
            type(self).__name__, collection, query, top_k, (filters, where)
        )

    @abstractmethod
    async def delete(self, collection: str, ids: list[str]) -> None:
        """Delete vector entries by ID from the specified collection.

        Args:
            collection: The collection.
            ids: The entry ids to delete.

        """

    @abstractmethod
    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete all vector entries for a specific document.

        Args:
            collection: The collection.
            document_id: The document whose entries are deleted.

        """

    @abstractmethod
    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Count the stored vector entries for a specific document.

        0 if none, or when the collection is absent.

        Args:
            collection: The collection.
            document_id: The document.

        Returns:
            How many entries the document has.

        """

    async def existing_ids(self, collection: str, ids: list[str]) -> set[str]:
        """Return the subset of ``ids`` already stored in ``collection``.

        An existence probe.

        Derived from ``stored_metadata``, so a store answers both probes by implementing
        that one.

        Args:
            collection: The collection.
            ids: The entry ids to check.

        Returns:
            The subset of ``ids`` already stored in ``collection``.

        """
        return set(await self.stored_metadata(collection, ids))

    @abstractmethod
    async def stored_metadata(
        self, collection: str, ids: list[str]
    ) -> dict[str, dict[str, str]]:
        """Return ``{id: stored metadata}`` for the ``ids`` already stored.

        Only the subset stored in ``collection``.

        The metadata is what the entry was upserted with (``VectorEntry.metadata``),
        without the entry's own ``id``/``document_id``/``content``. A store with no cheap
        probe returns ``{}`` ("nothing indexed"), so an incremental indexer just re-embeds
        every chunk (idempotent upsert — correct, only not resumable). Stores that CAN
        answer cheaply (e.g. Qdrant ``retrieve`` by id) return the stored metadata, so an
        at-least-once redrive skips the sub-batches already upserted with the same
        metadata instead of re-embedding them.

        Args:
            collection: The collection.
            ids: The entry ids to look up.

        Returns:
            Each stored id's metadata; an id that is not stored is absent.

        """


def _no_keyword_search(
    store: str,
    collection: str,
    query: str,
    top_k: int,
    narrowing: tuple[dict[str, str] | None, FilterExpr | None],
) -> NoReturn:
    """Fail a keyword search on a store without a keyword index.

    Args:
        store: The store's class name.
        collection: The collection searched.
        query: The query text (only its length is reported).
        top_k: The results asked for.
        narrowing: The ``filters`` and ``where`` asked for (only the filter names and
            whether a ``where`` was given are reported).

    Raises:
        InternalError: Always; the caller should have checked ``supports_keyword``.

    """
    filters, where = narrowing
    msg = (
        f"{store} has no keyword search (collection {collection!r}, "
        f"{top_k} hits, {len(query)} chars, filters {sorted(filters or {})}, "
        f"where {where is not None})"
    )
    raise InternalError(msg)
