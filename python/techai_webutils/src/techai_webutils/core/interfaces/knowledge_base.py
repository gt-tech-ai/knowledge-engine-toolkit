"""Knowledge base interface for managing document collections."""

from abc import ABC, abstractmethod
from collections.abc import Mapping
from dataclasses import dataclass

from techai_webutils.core.interfaces.lifecycle import ManagedResource


@dataclass
class KBDocument:
    """A document in the knowledge base."""

    id: str
    """Unique identifier of the document within the knowledge base."""
    file_name: str
    """The source file name, shown to users and in citations."""
    content_type: str
    """MIME type of the source file, used to select the parser."""
    status: str  # "pending", "processing", "ready", "error"
    """Indexing lifecycle status: ``pending``, ``processing``, ``ready``, or ``error``."""
    chunk_count: int = 0
    """Number of chunks this document was split into once indexed (0 before indexing)."""
    error_message: str | None = None
    """Failure detail when ``status`` is ``error`` (``None`` otherwise)."""


@dataclass
class KBSyncResult:
    """Result of a knowledge base sync operation."""

    documents_added: int
    """Count of documents newly indexed during the sync."""
    documents_updated: int
    """Count of already-indexed documents whose content was re-indexed."""
    documents_removed: int
    """Count of documents dropped from the index because their source no longer exists."""
    chunks_created: int
    """Total number of chunks produced across all added/updated documents."""
    errors: list[str]
    """Per-document error messages for documents that failed to sync (empty on full success)."""


class KnowledgeBase(ManagedResource, ABC):
    """Abstract knowledge base for managing document embeddings and retrieval.

    Document ids are unique across the knowledge base, not per scope: two tenants indexing the same id
    address the same document (index, status and removal all key on it). A consumer whose ids are only
    unique within a scope namespaces them (e.g. ``f"{tenant}:{document_id}"``). Any scope (tenant,
    sensitivity label) rides in the ``attributes`` a consumer stamps onto each chunk.
    """

    @abstractmethod
    async def index_document(
        self,
        document_id: str,
        chunks: list[str],
        *,
        attributes: Mapping[str, str] | None = None,
        document_name: str = "",
    ) -> None:
        """Index a document's chunks into the knowledge base.

        ``attributes`` are stamped onto every stored chunk as metadata (e.g. the consumer's tenant scope
        and sensitivity label), so retrieval can push them down and ``FilteringRetrievalEngine`` policies
        can re-validate them; re-indexing a document with other attributes restamps every chunk. An
        implementation refuses attribute names it writes itself (``ValueError``). ``document_name`` is
        the human-readable source name stamped onto each chunk for citation display; an implementation
        falls back to the ``document_id`` when it is empty.
        """

    @abstractmethod
    async def remove_document(self, document_id: str) -> None:
        """Remove a document and its chunks from the knowledge base."""

    @abstractmethod
    async def get_document_status(self, document_id: str) -> KBDocument | None:
        """Get the indexing status of a document."""

    @abstractmethod
    async def sync(self) -> KBSyncResult:
        """Synchronize the knowledge base with the latest document states."""
