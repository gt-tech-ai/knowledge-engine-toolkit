"""``Sink``: where an ingested document's chunks (and their vectors) are finally written.

The ingestion pipeline ends by handing one document's chunks to a sink: a search index that takes
vectors (OpenSearch), or a managed knowledge base that embeds by itself (Bedrock, ``vectors`` is
``None``). A sink also deletes a document and changes its metadata (clearance, tags) without
re-embedding it.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.vector_store import VectorEntry


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


class Sink(ManagedResource, ABC):
    """Writes, updates and deletes one document's chunks in the search backend."""

    @abstractmethod
    async def write(
        self,
        document: SinkDocument,
        chunks: Sequence[ChunkPayload],
        vectors: Sequence[Sequence[float]] | None,
    ) -> None:
        """Write ``document``'s chunks, with one vector per chunk unless the sink embeds itself."""
        ...

    @abstractmethod
    async def delete(self, org_id: str, document_id: str) -> None:
        """Delete every chunk of the document."""
        ...

    @abstractmethod
    async def update_metadata(
        self, org_id: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set ``fields`` on every chunk of the document without re-embedding it."""
        ...


class SinkStore(Protocol):
    """The vector-store slice a vector sink writes through (``OpenSearchVectorStore`` has it)."""

    @property
    def configured_model(self) -> tuple[str, str]:
        """The ``(model, version)`` the store stamps on the indices it creates."""
        ...

    def for_org(self, org_id: str) -> SinkStore:
        """Return a view scoped to ``org_id``."""
        ...

    async def upsert(self, collection: str, entries: Sequence[VectorEntry]) -> None:
        """Write entries by id."""
        ...

    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete a document's entries."""
        ...

    async def update_metadata(
        self, collection: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set fields on a document's entries."""
        ...

    async def current_index(self, collection: str) -> str | None:
        """Return the index the collection reads from, or ``None`` before the first write."""
        ...

    async def index_model(self, index: str) -> tuple[str, str]:
        """Return the ``(model, version)`` ``index`` was created for."""
        ...
