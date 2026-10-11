"""Vector store interface for similarity search."""

from abc import ABC, abstractmethod
from dataclasses import dataclass

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
    """Abstract vector store for embedding storage and similarity search (upsert/search/delete)."""

    @property
    @abstractmethod
    def dimension(self) -> int:
        """The vector dimension of collections this store manages (must match the embedder)."""

    @abstractmethod
    async def upsert(self, collection: str, entries: list[VectorEntry]) -> None:
        """Insert or update vector entries in the specified collection."""

    @abstractmethod
    async def search(
        self,
        collection: str,
        query_embedding: list[float],
        top_k: int = 10,
        filters: dict[str, str] | None = None,
    ) -> list[VectorSearchResult]:
        """Search for similar vectors in the specified collection."""

    @abstractmethod
    async def delete(self, collection: str, ids: list[str]) -> None:
        """Delete vector entries by ID from the specified collection."""

    @abstractmethod
    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete all vector entries for a specific document."""

    @abstractmethod
    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Count the stored vector entries for a specific document (0 if none/collection absent)."""

    async def existing_ids(self, collection: str, ids: list[str]) -> set[str]:
        """Return the subset of ``ids`` already stored in ``collection`` (an existence probe).

        Derived from ``stored_metadata``, so a store answers both probes by implementing that one.
        """
        return set(await self.stored_metadata(collection, ids))

    @abstractmethod
    async def stored_metadata(
        self, collection: str, ids: list[str]
    ) -> dict[str, dict[str, str]]:
        """Return ``{id: stored metadata}`` for the subset of ``ids`` already stored in ``collection``.

        The metadata is what the entry was upserted with (``VectorEntry.metadata``), without the
        entry's own ``id``/``document_id``/``content``. A store with no cheap probe returns ``{}``
        ("nothing indexed"), so an incremental indexer just re-embeds every chunk (idempotent
        upsert — correct, only not resumable). Stores that CAN answer cheaply (e.g. Qdrant
        ``retrieve`` by id) return the stored metadata, so an at-least-once redrive skips the
        sub-batches already upserted with the same metadata instead of re-embedding them.
        """
