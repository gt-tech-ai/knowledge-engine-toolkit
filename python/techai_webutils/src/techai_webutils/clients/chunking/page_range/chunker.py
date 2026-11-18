"""Chunk a paged document into runs of pages, cut at its own structure (``page_range``).

A chunk covers up to ``unit_pages`` pages. When the run reaches the last page the chunk
takes the rest of the document; otherwise it ends at the last markdown heading in the
second half of the run, else at the last paragraph break inside its last page, else
where that page ends, so a chunk reads as a section instead of stopping mid-sentence. With
``overlap_pages`` the next chunk starts that many pages back, so an answer that straddles
a cut is whole in one of them. Each chunk reports the first and last page it touches
(``StructuredChunk.page_start`` / ``page_end``). Text with no page spans, as from any
format but PDF, is one chunk with no page range. Deterministic: the same text and pages
always give the same chunks.
"""

from __future__ import annotations

import re
from bisect import bisect_right
from typing import TYPE_CHECKING, final

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.chunker import StructuredChunk

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.domain import PageSpan, Turn
    from techai_webutils.core.interfaces.annotator import Entity

_HEADING = re.compile(r"^#{1,6}[ \t]", re.MULTILINE)
"""A markdown ATX heading at the start of a line."""

_PARAGRAPH_BREAK = "\n\n"
"""What separates two paragraphs, and two pages of a joined document."""


@final
class PageRangeChunker:
    """A ``StructuredChunker`` that cuts a paged document into runs of pages."""

    def __init__(self, *, unit_pages: int, overlap_pages: int = 0) -> None:
        """Set the run length and overlap.

        Args:
            unit_pages: The most pages one chunk covers; at least 1.
            overlap_pages: Pages the next chunk repeats from the end of the previous one;
                0 up to ``unit_pages - 1``.

        Raises:
            AppValueError: ``unit_pages`` is below 1, or ``overlap_pages`` is negative or
                not below ``unit_pages``.

        """
        if unit_pages < 1:
            msg = f"page_range chunker: unit_pages must be at least 1, got {unit_pages}"
            raise AppValueError(msg)
        if not 0 <= overlap_pages < unit_pages:
            msg = (
                "page_range chunker: overlap_pages must be from 0 to unit_pages - 1 "
                f"({unit_pages - 1}), got {overlap_pages}"
            )
            raise AppValueError(msg)
        self._unit_pages = unit_pages
        self._overlap_pages = overlap_pages

    def chunk(self, text: str) -> list[str]:
        """Return ``text`` as one chunk, as no page spans are known here.

        Args:
            text: The document text.

        Returns:
            One stripped chunk, or none when ``text`` is blank.

        """
        return [c.text for c in self.chunk_spans(text)]

    def chunk_spans(
        self,
        text: str,
        *,
        pages: Sequence[PageSpan] = (),
        entities: Sequence[Entity] = (),
        turns: Sequence[Turn] = (),
        language: str | None = None,
    ) -> list[StructuredChunk]:
        """Return ``text`` cut into runs of pages, each with its page range.

        Args:
            text: The document text (a parsed document's markdown).
            pages: Where each page's text sits in ``text`` (``page_spans``); with none,
                the whole text is one chunk without a page range.
            entities: Unused; the interface's argument.
            turns: Unused; the interface's argument.
            language: Unused; the interface's argument.

        Returns:
            The chunks in order, each carrying ``page_start`` / ``page_end``.

        """
        del entities, turns, language
        if not text.strip():
            return []
        if not pages:
            return [_chunk(text, _skip_blank(text, 0), len(text), None)]
        starts = [p.start for p in pages]
        chunks: list[StructuredChunk] = []
        pos = _skip_blank(text, 0)
        while pos < len(text):
            first = _page_index(starts, pos)
            last_of_run = first + self._unit_pages - 1
            if last_of_run >= len(pages) - 1:
                cut = len(text)
            else:
                cut = _cut(text, pos, pages[last_of_run], pages[last_of_run + 1].start)
            chunk = _chunk(text, pos, cut, pages, starts)
            chunks.append(chunk)
            if cut >= len(text):
                break
            pos = _skip_blank(text, self._next_start(chunk, first, cut, pages, starts))
        return chunks

    def _next_start(
        self,
        chunk: StructuredChunk,
        first: int,
        cut: int,
        pages: Sequence[PageSpan],
        starts: list[int],
    ) -> int:
        """Return where the chunk after ``chunk`` starts.

        With no overlap, or when the chunk is too short to repeat any page, that is where
        the chunk was cut; otherwise the start of the page ``overlap_pages`` back from the
        chunk's last.

        Args:
            chunk: The chunk just built.
            first: Index of the page the chunk starts on.
            cut: Where the chunk was cut, before its trailing whitespace was stripped.
            pages: The document's page spans.
            starts: Each page's start offset.

        Returns:
            The next chunk's start offset.

        """
        if self._overlap_pages == 0:
            return cut
        last = _page_index(starts, chunk.end - 1)
        repeat_from = last - self._overlap_pages + 1
        return pages[repeat_from].start if repeat_from > first else cut


def _skip_blank(text: str, pos: int) -> int:
    """Return the offset of the first non-whitespace character at or after ``pos``.

    Args:
        text: The document text.
        pos: Where to start looking.

    Returns:
        That offset, or ``len(text)`` when only whitespace remains.

    """
    while pos < len(text) and text[pos].isspace():
        pos += 1
    return pos


def _page_index(starts: list[int], offset: int) -> int:
    """Return the index of the last page starting at or before ``offset``.

    Args:
        starts: Each page's start offset, ascending.
        offset: A text offset.

    Returns:
        The page's index; 0 for an offset before the first page.

    """
    return max(bisect_right(starts, offset) - 1, 0)


def _cut(text: str, pos: int, last_page: PageSpan, next_start: int) -> int:
    """Return where a run starting at ``pos`` and ending with ``last_page`` is cut.

    The last heading in the run's second half, else the last paragraph break inside
    ``last_page``, else the end of ``last_page`` (the joins between pages are not
    paragraph breaks of their own, or every run would end a page early). When ``pos``
    sits in text no page span claims, past ``last_page``'s end, the cut is the start of
    the next page, so that text forms its own chunk and the chunker moves on.

    Args:
        text: The document text.
        pos: Where the run starts.
        last_page: The run's last page.
        next_start: Where the page after ``last_page`` starts; always after ``pos``.

    Returns:
        The offset the chunk is cut at; always after ``pos``.

    """
    midpoint = pos + (last_page.end - pos) // 2
    headings = [m.start() for m in _HEADING.finditer(text, midpoint, last_page.end)]
    heading = next((h for h in reversed(headings) if h > pos), None)
    if heading is not None:
        return heading
    paragraph = text.rfind(_PARAGRAPH_BREAK, max(pos + 1, last_page.start), last_page.end)
    if paragraph > pos:
        return paragraph + len(_PARAGRAPH_BREAK)
    return last_page.end if last_page.end > pos else next_start


def _chunk(
    text: str,
    start: int,
    cut: int,
    pages: Sequence[PageSpan] | None,
    starts: list[int] | None = None,
) -> StructuredChunk:
    """Build the chunk of ``text[start:cut]``, stripped, with the pages it touches.

    Args:
        text: The document text.
        start: Offset of the chunk's first character (not whitespace).
        cut: Offset where the chunk was cut.
        pages: The document's page spans, or ``None`` for a document without pages.
        starts: Each page's start offset (``None`` when ``pages`` is).

    Returns:
        The chunk.

    """
    end = start + len(text[start:cut].rstrip())
    if pages is None or starts is None:
        return StructuredChunk(text[start:end], start, end)
    return StructuredChunk(
        text[start:end],
        start,
        end,
        page_start=pages[_page_index(starts, start)].page_number,
        page_end=pages[_page_index(starts, end - 1)].page_number,
    )
