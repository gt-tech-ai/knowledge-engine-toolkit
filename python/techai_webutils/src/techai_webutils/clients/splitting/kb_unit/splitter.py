"""KB-unit document splitter: split one parse into N index units.

A large parsed document is split into contiguous chunks each within ``max_bytes`` (the KB
per-unit ceiling / embedding context budget), so each chunk becomes its own index unit that
fits the provider's per-file limit. PDFs split on page boundaries (carrying the source page
range for citation provenance); non-paginated content splits on paragraph boundaries. The
split is provider-neutral — it produces plain ``ChunkPayload`` payloads the indexer then indexes
via the env-selected action (Bedrock sidecar+queue OR vector embed+upsert).

Invariants: chunks are contiguous and non-overlapping; every chunk is within ``max_bytes``
EXCEPT an indivisible single page/paragraph larger than the ceiling (emitted alone with a
warning); and the split always yields at least one chunk (a document is never dropped).
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.domain import ChunkPayload
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from techai_webutils.core.domain import Page, ParsedDocument

logger = get_logger(__name__)

# Paragraph separator for the non-paginated split + the join between a chunk's pages.
_SEPARATOR = "\n\n"
"""Paragraph separator for the non-paginated split and the join between a chunk's pages."""


def split_into_chunks(parsed: ParsedDocument, *, max_bytes: int) -> list[ChunkPayload]:
    """Split a parsed document into contiguous chunks each within ``max_bytes`` (>= 1 chunk).

    PDFs (``parsed.pages`` populated) split on page boundaries, carrying the source page range;
    other formats split ``parsed.markdown_content`` on paragraph boundaries with no page range.
    """
    if parsed.pages:
        return _split_pages(parsed.pages, max_bytes)
    return _split_markdown(parsed.markdown_content, max_bytes)


def _split_pages(pages: list[Page], max_bytes: int) -> list[ChunkPayload]:
    """Pack contiguous pages into chunks within ``max_bytes``, emitting an oversized page alone."""
    chunks: list[ChunkPayload] = []
    buffer: list[str] = []
    buffer_bytes = 0
    start_page: int | None = None
    end_page: int | None = None
    separator_bytes = len(_SEPARATOR.encode("utf-8"))

    for page in pages:
        page_bytes = len(page.text.encode("utf-8"))
        added_bytes = page_bytes + (separator_bytes if buffer else 0)
        # Flush the current buffer before this page would push it over the ceiling.
        if buffer and buffer_bytes + added_bytes > max_bytes:
            chunks.append(_page_chunk(len(chunks), buffer, start_page, end_page))
            buffer, buffer_bytes, start_page, end_page = [], 0, None, None
            added_bytes = page_bytes  # buffer is now empty — no separator
        if start_page is None:
            start_page = page.page_number
        buffer.append(page.text)
        buffer_bytes += added_bytes
        end_page = page.page_number
        # An indivisible page larger than the ceiling is emitted alone (can't be split here).
        if len(buffer) == 1 and page_bytes > max_bytes:
            logger.warning(
                "large_doc_chunker: page exceeds max_bytes; emitting oversized single-page chunk",
                page_number=page.page_number,
                page_bytes=page_bytes,
                max_bytes=max_bytes,
            )
            chunks.append(_page_chunk(len(chunks), buffer, start_page, end_page))
            buffer, buffer_bytes, start_page, end_page = [], 0, None, None

    if buffer:
        chunks.append(_page_chunk(len(chunks), buffer, start_page, end_page))
    if not chunks:
        chunks.append(
            ChunkPayload(chunk_index=0, text="", page_start=None, page_end=None)
        )
    return chunks


def _page_chunk(
    index: int, texts: list[str], start_page: int | None, end_page: int | None
) -> ChunkPayload:
    """Build a page-range chunk from its accumulated page texts."""
    return ChunkPayload(
        chunk_index=index,
        text=_SEPARATOR.join(texts),
        page_start=start_page,
        page_end=end_page,
    )


def _split_markdown(markdown: str, max_bytes: int) -> list[ChunkPayload]:
    """Pack contiguous paragraphs into chunks within ``max_bytes`` (no page range)."""
    chunks: list[ChunkPayload] = []
    buffer: list[str] = []
    buffer_bytes = 0
    separator_bytes = len(_SEPARATOR.encode("utf-8"))

    for paragraph in markdown.split(_SEPARATOR):
        paragraph_bytes = len(paragraph.encode("utf-8"))
        added_bytes = paragraph_bytes + (separator_bytes if buffer else 0)
        if buffer and buffer_bytes + added_bytes > max_bytes:
            chunks.append(_markdown_chunk(len(chunks), buffer))
            buffer, buffer_bytes = [], 0
            added_bytes = paragraph_bytes
        buffer.append(paragraph)
        buffer_bytes += added_bytes
        # An indivisible paragraph larger than the ceiling is emitted alone.
        if len(buffer) == 1 and paragraph_bytes > max_bytes:
            logger.warning(
                "large_doc_chunker: paragraph exceeds max_bytes; emitting oversized single chunk",
                paragraph_bytes=paragraph_bytes,
                max_bytes=max_bytes,
            )
            chunks.append(_markdown_chunk(len(chunks), buffer))
            buffer, buffer_bytes = [], 0

    if buffer:
        chunks.append(_markdown_chunk(len(chunks), buffer))
    if not chunks:
        chunks.append(
            ChunkPayload(chunk_index=0, text="", page_start=None, page_end=None)
        )
    return chunks


def _markdown_chunk(index: int, paragraphs: list[str]) -> ChunkPayload:
    """Build a non-paginated chunk from its accumulated paragraphs."""
    return ChunkPayload(
        chunk_index=index,
        text=_SEPARATOR.join(paragraphs),
        page_start=None,
        page_end=None,
    )


class KbUnitSplitter:
    """The ``kb_unit`` ``DocumentSplitter`` backend — split a parse into KB-sized index units.

    ``max_bytes`` (the per-unit ceiling) is injected at construction; ``split`` is the pure
    per-document transform (delegates to ``split_into_chunks``), so a huge ceiling is a pass-through
    (one unit) and a small one splits a large parse into N contiguous units.
    """

    def __init__(self, max_bytes: int) -> None:
        """Bind the per-index-unit byte ceiling this splitter packs chunks up to."""
        self._max_bytes = max_bytes

    def split(self, parsed: ParsedDocument) -> list[ChunkPayload]:
        """Return the parse's index units (>= 1), each within the configured ``max_bytes`` ceiling."""
        return split_into_chunks(parsed, max_bytes=self._max_bytes)
