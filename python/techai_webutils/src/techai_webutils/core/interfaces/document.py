"""Document parsing and chunking interfaces."""

from abc import ABC, abstractmethod
from dataclasses import dataclass


@dataclass
class ParsedDocument:
    """Result of parsing a document file."""

    content: str
    """The full extracted plain text of the document."""
    metadata: dict[str, str]
    """Parser-extracted document metadata (title, author, source format, and similar)."""
    page_count: int
    """Number of pages the source document contained (0 for non-paginated formats)."""
    word_count: int
    """Total word count across the extracted text."""


@dataclass
class DocumentChunk:
    """A chunk of a parsed document."""

    id: str
    """Stable identifier for this chunk within its parent document."""
    content: str
    """The chunk's text — the retrieval-sized slice that gets embedded and indexed."""
    page_number: int | None
    """Source page this chunk was drawn from (``None`` for non-paginated content)."""
    start_offset: int
    """Character offset where this chunk begins in the parent document's text."""
    end_offset: int
    """Character offset where this chunk ends in the parent document's text."""
    metadata: dict[str, str]
    """Per-chunk metadata carried alongside the content (e.g. section, heading)."""


class DocumentParser(ABC):
    """Abstract document parser for extracting text from various file formats."""

    @abstractmethod
    async def parse(self, content: bytes, content_type: str) -> ParsedDocument:
        """Parse raw file content into structured text."""

    @abstractmethod
    def supported_types(self) -> list[str]:
        """Return the list of MIME types this parser can handle."""


class ChunkingStrategy(ABC):
    """Abstract chunking strategy for splitting documents into retrieval-sized pieces."""

    @abstractmethod
    def chunk(self, document: ParsedDocument) -> list[DocumentChunk]:
        """Split a parsed document into chunks."""

    @abstractmethod
    def chunk_size(self) -> int:
        """Return the target chunk size in characters."""

    @abstractmethod
    def overlap(self) -> int:
        """Return the overlap size between adjacent chunks in characters."""
