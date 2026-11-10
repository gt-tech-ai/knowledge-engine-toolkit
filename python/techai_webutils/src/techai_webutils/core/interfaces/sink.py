"""``Sink``: where an ingested document's chunks (and their vectors) are finally written.

The ingestion pipeline ends by handing one document's chunks to a sink: a search index
that takes vectors (OpenSearch), or a managed knowledge base that embeds by itself
(Bedrock, ``vectors`` is ``None``). A sink also deletes a document and changes its
metadata (clearance, tags) without re-embedding it.

Every sink names chunk ``n`` of a document ``chunk_id(document_id, n)``, so a passage
cites the same chunk whichever backend served it.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.vector_store import VectorEntry


CHUNK_ID_KEY = "chunk_id"
"""The metadata key a sink stores a chunk's id under where the backend has no id of its
own (the Bedrock chunk-file sidecar); the citation extractor reads it back."""


def chunk_id(document_id: str, n: int) -> str:
    """Return the id of chunk ``n`` (0-based, in document order) of ``document_id``.

    ``<document>:<n>``.

    Args:
        document_id: The document.
        n: The chunk's position in the document, from 0.

    Returns:
        The id of chunk ``n`` (0-based, in document order) of ``document_id``.

    """
    return f"{document_id}:{n}"


@dataclass(frozen=True, slots=True)
class SinkDocument:
    """The document a sink write belongs to."""

    org_id: str
    """The org that owns the document; every write, update and delete is scoped to it."""
    document_id: str
    """The document's id."""
    content_hash: str
    """The hash of the bytes the chunks came from; stored on every chunk."""
    embedding_model: str
    """The model that produced the vectors (empty when the sink embeds by itself)."""
    embedding_version: str
    """The version of ``embedding_model``."""
    metadata: Mapping[str, str] = field(default_factory=dict[str, str])
    """Fields stored on every chunk (clearance, tags, name)."""


@dataclass(frozen=True, slots=True)
class ChunkPayload:
    """One chunk to write."""

    text: str
    """The chunk's text."""
    section_path: str = ""
    """Where the chunk sits in the document (``Intro > Scope``)."""
    element_type: str = ""
    """The structural element the chunk came from (``paragraph``, ``table``, ``turn``)."""
    language: str = ""
    """The chunk's language code."""
    entities: Mapping[str, tuple[str, ...]] = field(
        default_factory=dict[str, tuple[str, ...]]
    )
    """Entity label → the entities of that label found in the chunk."""
    metadata: Mapping[str, str] = field(default_factory=dict[str, str])
    """Further fields stored on this chunk only."""
    ordinal: int | None = None
    """The chunk's position in the document (its id is ``chunk_id(document, ordinal)``);
    None means its position in the write. A caller writing a subset of a document's chunks
    (those it embedded) sets it, so every chunk keeps the id it has in the whole
    document."""


class Sink(ManagedResource, ABC):
    """Writes, updates and deletes one document's chunks in the search backend."""

    @abstractmethod
    async def write(
        self,
        document: SinkDocument,
        chunks: Sequence[ChunkPayload],
        vectors: Sequence[Sequence[float]] | None,
    ) -> None:
        """Write ``document``'s chunks.

        With one vector per chunk unless the sink embeds itself.

        Args:
            document: The document the chunks belong to.
            chunks: The chunks, in document order.
            vectors: One embedding per chunk, in order; ``None`` for a sink that does not
                store vectors.

        """
        ...

    @abstractmethod
    async def delete(self, org_id: str, document_id: str) -> None:
        """Delete every chunk of the document.

        Args:
            org_id: The tenant that owns the document.
            document_id: The document whose chunks are deleted.

        """
        ...

    @abstractmethod
    async def update_metadata(
        self, org_id: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set ``fields`` on every chunk of the document without re-embedding it.

        Args:
            org_id: The tenant that owns the document.
            document_id: The document whose chunks are updated.
            fields: The metadata fields to set.

        """
        ...


class SinkStore(Protocol):
    """The vector-store slice a vector sink writes through.

    (``OpenSearchVectorStore`` has it).
    """

    @property
    def configured_model(self) -> tuple[str, str]:
        """The ``(model, version)`` the store stamps on the indices it creates."""
        ...

    def for_org(self, org_id: str) -> SinkStore:
        """Return a view scoped to ``org_id``.

        Args:
            org_id: The tenant.

        Returns:
            A store whose reads and writes stay within ``org_id``.

        """
        ...

    async def upsert(self, collection: str, entries: Sequence[VectorEntry]) -> None:
        """Write entries by id.

        Args:
            collection: The logical collection.
            entries: The entries to write; an existing id is overwritten.

        """
        ...

    async def delete_by_document(
        self, collection: str, document_id: str, keep: Sequence[str] = ()
    ) -> None:
        """Delete a document's entries, except the ids in ``keep``.

        Args:
            collection: The logical collection.
            document_id: The document whose entries are deleted.
            keep: Entry ids to keep.

        """
        ...

    async def stored_metadata(
        self, collection: str, ids: Sequence[str]
    ) -> dict[str, dict[str, str]]:
        """Return ``{id: stored metadata}`` for the ``ids`` stored where writes go.

        Args:
            collection: The logical collection.
            ids: The entry ids to look up.

        Returns:
            Each stored id's metadata; an id that is not stored is absent.

        """
        ...

    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Return how many entries the document has.

        Args:
            collection: The logical collection.
            document_id: The document.

        Returns:
            How many entries the document has.

        """
        ...

    async def update_metadata(
        self, collection: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set fields on a document's entries.

        Args:
            collection: The logical collection.
            document_id: The document whose entries are updated.
            fields: The metadata fields to set.

        """
        ...

    async def current_index(self, collection: str) -> str | None:
        """Return the index the collection reads from.

        Or ``None`` before the first write.

        Args:
            collection: The logical collection.

        Returns:
            The physical index the collection reads from, or ``None`` when it has none
                yet.

        """
        ...

    async def index_model(self, index: str) -> tuple[str, str]:
        """Return the ``(model, version)`` ``index`` was created for.

        Args:
            index: A physical index.

        Returns:
            The embedding model and version the index was created for.

        """
        ...
