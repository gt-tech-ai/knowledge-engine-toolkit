"""Vector store interface for similarity search.

(and, where the store has one, keyword search).
"""

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import NoReturn

from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.lifecycle import ManagedResource


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
    """Metadata stored with the chunk (used for filtering and citation display)."""


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
    metadata: dict[str, str]
    """Metadata stored with the entry, available for filtered search and citations."""


class VectorStore(ManagedResource, ABC):
    """Abstract vector store for embedding storage and similarity search.

    (upsert/search/delete).
    """

    @property
    @abstractmethod
    def dimension(self) -> int:
        """The vector dimension of collections this store manages.

        (must match the embedder).
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
    ) -> list[VectorSearchResult]:
        """Search for similar vectors in the specified collection.

        Args:
            collection: The collection.
            query_embedding: The query's vector.
            top_k: The most results to return.
            filters: Metadata every result must match; ``None`` matches all.

        Returns:
            The nearest entries with their scores, best first.

        """

    @property
    def supports_keyword(self) -> bool:
        """Whether ``keyword_search`` is available (a BM25 index beside the vectors).

        ``False`` by default.
        """
        return False

    async def keyword_search(
        self,
        collection: str,
        query: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
    ) -> list[VectorSearchResult]:
        """Search the stored text lexically (BM25).

        Only stores with ``supports_keyword`` implement it; this default raises
        ``InternalError``, so callers check ``supports_keyword`` first.

        Args:
            collection: The collection.
            query: The query text.
            top_k: The most results to return.
            filters: Metadata every result must match; ``None`` matches all.

        Returns:
            The best lexical matches with their BM25 scores, best first.

        """
        return _no_keyword_search(type(self).__name__, collection, query, top_k, filters)

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
        """Count the stored vector entries.

        For a specific document (0 if none/collection absent).

        Args:
            collection: The collection.
            document_id: The document.

        Returns:
            How many entries the document has.

        """

    async def existing_ids(self, collection: str, ids: list[str]) -> set[str]:
        """Return the subset of ``ids`` already stored in ``collection``.

        (an existence probe).

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
        """Return ``{id: stored metadata}`` for the subset of ``ids`` already stored.

        In ``collection``.

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
    store: str, collection: str, query: str, top_k: int, filters: dict[str, str] | None
) -> NoReturn:
    """Fail a keyword search on a store without a keyword index.

    Args:
        store: The store's class name.
        collection: The collection searched.
        query: The query text (only its length is reported).
        top_k: The results asked for.
        filters: The filters asked for (only their names are reported).

    Raises:
        InternalError: Always; the caller should have checked ``supports_keyword``.

    """
    msg = (
        f"{store} has no keyword search (collection {collection!r}, "
        f"{top_k} hits, {len(query)} chars, filters {sorted(filters or {})})"
    )
    raise InternalError(msg)
