"""Parser domain DTOs: supported formats, per-page text, and the parsed-document result.

These are the shared, dependency-free contracts a ``DocumentParser`` backend produces
(``techai_webutils.core.interfaces.parser``). They live in this shared library — not an app — so any
deployable (a document-processing service, a stand-alone parse service) programs against
the same DTO, exactly like Go's ``go/core`` types. Frozen ``slots`` dataclasses of scalars/lists
so a boundary DTO is "a queue message waiting to happen" (extraction-ready).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum


class DocumentFormat(StrEnum):
    """A document format the ingestion parsers understand."""

    PDF = "pdf"
    """Portable Document Format; parsed page-by-page so citations can address a page."""
    DOCX = "docx"
    """Microsoft Word document (OOXML, ZIP-packaged)."""
    PPTX = "pptx"
    """Microsoft PowerPoint presentation (OOXML, ZIP-packaged)."""
    XLSX = "xlsx"
    """Microsoft Excel workbook (OOXML, ZIP-packaged)."""
    XLS = "xls"
    """Legacy Microsoft Excel workbook (OLE2 binary)."""
    MD = "md"
    """Markdown text."""
    TXT = "txt"
    """Plain UTF-8 text."""
    HTML = "html"
    """HTML markup."""
    CSV = "csv"
    """Comma-separated values."""
    UNKNOWN = "unknown"
    """Format could not be detected or is unsupported by the parsers."""


@dataclass(frozen=True, slots=True)
class Page:
    """One page of a paginated document (PDF only) for citation provenance."""

    # page_number is the 1-based page index.
    page_number: int
    """The 1-based page index within the source document."""

    # text is the extracted plain text of the page.
    text: str
    """The extracted plain text of this page."""


@dataclass(frozen=True, slots=True)
class ParsedDocument:
    """The result of parsing a document into Markdown-structured text.

    ``pages`` is populated only for PDFs (page-level text + boundaries for citation
    provenance); other formats rely on ``markdown_content`` alone. ``error`` is non-empty
    when parsing failed or only partially succeeded (the parser never raises to the caller
    for a bad document — it returns a result carrying the error instead).
    """

    # markdown_content is the full document converted to Markdown ("" on failure).
    markdown_content: str
    """The full document converted to Markdown (empty string on failure)."""

    # document_format is the resolved source format (UNKNOWN when undetected/unsupported).
    document_format: DocumentFormat
    """The resolved source format (``UNKNOWN`` when undetected or unsupported)."""

    # total_pages is the page count (PDF only; 0 otherwise). Equals len(pages).
    total_pages: int = 0
    """Page count for PDFs (0 for non-paginated formats); equals ``len(pages)``."""

    # word_count is the whitespace-split token count of markdown_content.
    word_count: int = 0
    """Whitespace-split token count of ``markdown_content``."""

    # pages holds per-page text for PDFs (empty for other formats).
    pages: list[Page] = field(default_factory=list)
    """Per-page text for PDFs (citation provenance); empty for other formats."""

    # error is a short failure tag (e.g. "unsupported_format", "conversion_failed: ..."),
    # empty on success.
    error: str = ""
    """Short failure tag (e.g. ``unsupported_format``, ``conversion_failed: ...``); empty on success."""

    @property
    def ok(self) -> bool:
        """Whether the document parsed without error."""
        return not self.error
