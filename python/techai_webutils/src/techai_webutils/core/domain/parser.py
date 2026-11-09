"""Parser domain DTOs: supported formats, per-page text, and the parsed-document result.

These are the shared, dependency-free contracts a ``DocumentParser`` backend produces
(``techai_webutils.core.interfaces.parser``). They live in this shared library — not an app — so any
deployable (a document-processing service, a stand-alone parse service) programs against
the same DTO, exactly like Go's ``go/core`` types. Frozen ``slots`` dataclasses of scalars/lists
so a boundary DTO is "a queue message waiting to happen" (extraction-ready).

``parsed_to_json`` / ``parsed_from_json`` give a ``ParsedDocument``'s JSON form (the parse stage's
checkpoint artifact and hand-off), tagged with ``PARSED_FORMAT_VERSION``; a reader accepts its own
version and the one before (N/N-1). ``join_parsed`` joins the parts of a document parsed in page
ranges (``PagedParser``) back into one.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.domain.document_ir import blocks_from_json, blocks_to_json
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.domain.document_ir import DocumentBlock

PARSED_FORMAT_VERSION = 1
"""The version ``parsed_to_json`` writes."""


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
    provenance); other formats rely on ``markdown_content`` alone. ``blocks`` holds the typed
    blocks in reading order when a layout-aware parser produced them (empty otherwise). ``error`` is non-empty
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

    # blocks are the typed blocks in reading order (the ``layout`` parser only).
    blocks: tuple[DocumentBlock, ...] = ()
    """Typed blocks in reading order (``core.domain.document_ir``); empty unless a layout-aware
    parser produced them."""

    attributes: Mapping[str, str] = field(default_factory=dict[str, str])
    """Source facts a later stage needs and no other field holds (an interview's id and
    privacy mode); empty for an ordinary document."""

    @property
    def ok(self) -> bool:
        """Whether the document parsed without error."""
        return not self.error


def join_parsed(parts: Sequence[ParsedDocument]) -> ParsedDocument:
    """Join the page-range parts of one document into the whole, in page order.

    Parts are ordered by their first page; markdown, pages and blocks concatenate, block ``order``
    is renumbered so it stays unique, and word counts add. The first part's error, if any, fails
    the joined document (a missing range must not pass as a shorter document).

    Args:
        parts: The parsed page ranges of one document, in any order.

    Returns:
        The joined document.

    Raises:
        AppValueError: ``parts`` is empty.

    """
    if not parts:
        msg = "join_parsed needs at least one part"
        raise AppValueError(msg)
    ordered = sorted(parts, key=_first_page)
    pages = [page for part in ordered for page in part.pages]
    blocks = tuple(
        replace(block, order=order)
        for order, block in enumerate(b for part in ordered for b in part.blocks)
    )
    return ParsedDocument(
        markdown_content="\n\n".join(
            p.markdown_content for p in ordered if p.markdown_content
        ),
        document_format=ordered[0].document_format,
        total_pages=max(len(pages), *(p.total_pages for p in ordered)),
        word_count=sum(p.word_count for p in ordered),
        pages=pages,
        error=next((p.error for p in ordered if p.error), ""),
        blocks=blocks,
    )


def _first_page(part: ParsedDocument) -> int:
    """Return a part's first page number (its first page, else its first paged block, else 0)."""
    if part.pages:
        return part.pages[0].page_number
    return next((b.page for b in part.blocks if b.page is not None), 0)


def parsed_to_json(doc: ParsedDocument) -> dict[str, object]:
    """Return ``doc`` as a JSON-ready dict tagged with ``PARSED_FORMAT_VERSION``."""
    return {
        "format_version": PARSED_FORMAT_VERSION,
        "markdown_content": doc.markdown_content,
        "document_format": str(doc.document_format),
        "total_pages": doc.total_pages,
        "word_count": doc.word_count,
        "pages": [[p.page_number, p.text] for p in doc.pages],
        "error": doc.error,
        "blocks": blocks_to_json(doc.blocks),
        "attributes": dict(doc.attributes),
    }


def parsed_from_json(payload: dict[str, object]) -> ParsedDocument:
    """Read a document written by ``parsed_to_json`` at this version or the one before.

    Raises:
        AppValueError: A ``format_version`` other than N or N-1, or a field of the wrong
            type (``INVALID_INPUT``).

    """
    version = payload.get("format_version")
    if version not in {PARSED_FORMAT_VERSION, PARSED_FORMAT_VERSION - 1}:
        msg = f"unsupported parsed-document format_version {version!r}"
        raise AppValueError(msg)
    try:
        pages = cast("list[list[object]]", payload.get("pages", []))
        return ParsedDocument(
            markdown_content=str(payload["markdown_content"]),
            document_format=DocumentFormat(str(payload["document_format"])),
            total_pages=int(cast("int", payload.get("total_pages", 0))),
            word_count=int(cast("int", payload.get("word_count", 0))),
            pages=[Page(int(cast("int", n)), str(text)) for n, text in pages],
            error=str(payload.get("error", "")),
            blocks=_blocks(payload.get("blocks")),
            attributes={
                str(k): str(v)
                for k, v in cast(
                    "dict[str, object]", payload.get("attributes", {})
                ).items()
            },
        )
    except (KeyError, TypeError, ValueError) as exc:
        msg = f"malformed parsed document: {exc}"
        raise AppValueError(msg) from exc


def _blocks(raw: object) -> tuple[DocumentBlock, ...]:
    """Return the blocks of a ``parsed_to_json`` payload; an absent field is no blocks."""
    if raw is None:
        return ()
    return blocks_from_json(cast("dict[str, object]", raw))
