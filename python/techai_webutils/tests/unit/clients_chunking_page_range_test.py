"""Unit tests for the ``page_range`` chunker and its factory validation."""

from __future__ import annotations

from itertools import starmap
from typing import TYPE_CHECKING, cast

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from techai_webutils.clients.chunking import (
    ChunkerConfig,
    ChunkerKind,
    chunker_from_config,
)
from techai_webutils.core.domain import (
    DocumentFormat,
    Page,
    ParsedDocument,
    page_spans,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.chunker import Chunker, StructuredChunk

if TYPE_CHECKING:
    from techai_webutils.clients.chunking.page_range import PageRangeChunker

_TEN_PAGES = 10
_UNIT_PAGES = 4
_MAX_PAGE_CHARS = 30
_MAX_PAGES = 12
_MAX_UNIT_PAGES = 5


def _document(texts: list[str]) -> ParsedDocument:
    """Return a PDF whose page ``n`` holds ``texts[n - 1]``, pages joined by a blank line.

    Args:
        texts: The text of each page, in order.

    Returns:
        The document, its markdown the pages joined the way ``join_parsed`` joins parts.

    """
    return ParsedDocument(
        "\n\n".join(texts),
        DocumentFormat.PDF,
        total_pages=len(texts),
        pages=list(starmap(Page, enumerate(texts, start=1))),
    )


def _chunks(
    document: ParsedDocument, unit_pages: int, overlap_pages: int = 0
) -> list[StructuredChunk]:
    """Chunk ``document`` by page ranges through the factory.

    Args:
        document: The parsed document.
        unit_pages: Pages per chunk.
        overlap_pages: Pages repeated at the start of the next chunk.

    Returns:
        Its chunks, each carrying the page range it covers.

    """
    config = ChunkerConfig(
        kind=ChunkerKind.PAGE_RANGE, unit_pages=unit_pages, overlap_pages=overlap_pages
    )
    chunker = cast("PageRangeChunker", chunker_from_config(config))
    return chunker.chunk_spans(document.markdown_content, pages=page_spans(document))


def _ranges(chunks: list[StructuredChunk]) -> list[tuple[int | None, int | None]]:
    """Return each chunk's ``(page_start, page_end)``.

    Args:
        chunks: The chunks.

    Returns:
        Their page ranges, in order.

    """
    return [(c.page_start, c.page_end) for c in chunks]


def test_factory_builds_page_range_and_refuses_bad_page_settings() -> None:
    """Test that the factory builds the ``page_range`` kind and validates its dials.

    **Why this test is important:**
      - ``unit_pages`` defaults to 0 (off), so a config that selects the kind but never
        sets it would otherwise build a chunker that cuts nothing; the error must name
        the field so the operator can fix the config rather than guess.

    **What it tests:**
      - ``ChunkerKind.PAGE_RANGE`` with valid dials returns a ``Chunker``.
      - ``unit_pages`` of 0 or less raises ``AppValueError`` naming ``unit_pages``.
      - ``overlap_pages`` equal to or above ``unit_pages``, or negative, raises
        ``AppValueError`` naming ``overlap_pages``.
    """
    built = chunker_from_config(
        ChunkerConfig(kind=ChunkerKind.PAGE_RANGE, unit_pages=4, overlap_pages=1)
    )
    assert isinstance(built, Chunker)

    with pytest.raises(AppValueError, match="unit_pages"):
        _ = chunker_from_config(ChunkerConfig(kind=ChunkerKind.PAGE_RANGE))
    for overlap in (4, 5, -1):
        with pytest.raises(AppValueError, match="overlap_pages"):
            _ = chunker_from_config(
                ChunkerConfig(
                    kind=ChunkerKind.PAGE_RANGE, unit_pages=4, overlap_pages=overlap
                )
            )


def test_page_range_cuts_at_heading_then_paragraph_then_page_boundary() -> None:
    """Test that a chunk ends at a heading, else a paragraph, else a page boundary.

    **Why this test is important:**
      - A unit that stops mid-sentence embeds badly and cites a page range that hides
        where the answer starts; cutting at the document's own structure keeps each unit
        a readable section, and the page range on it is what a citation links to.

    **What it tests:**
      - With 4 pages per chunk and a heading late in the fourth page, the first chunk
        ends just before that heading and the next starts at it.
      - With no heading but a blank line inside the fourth page, the cut is at that
        paragraph break.
      - With neither, the chunk ends where the fourth page does.
      - Each chunk carries the first and last page it touches, and a short document is
        one chunk covering all its pages.
    """
    filler = ["x" * 20] * 3
    with_heading = _document([*filler, "intro line\n\n# Next section\nbody", *filler])
    chunks = _chunks(with_heading, _UNIT_PAGES)
    assert [c.text.startswith("# Next section") for c in chunks] == [False, True]
    assert chunks[0].text.endswith("intro line")
    assert _ranges(chunks) == [(1, 4), (4, 7)]

    with_paragraph = _document([*filler, "first para\n\nsecond para", *filler])
    chunks = _chunks(with_paragraph, _UNIT_PAGES)
    assert chunks[0].text.endswith("first para")
    assert chunks[1].text.startswith("second para")

    plain = _document([f"page {n}" for n in range(1, _TEN_PAGES + 1)])
    chunks = _chunks(plain, _UNIT_PAGES)
    assert _ranges(chunks) == [(1, 4), (5, 8), (9, 10)]

    assert _ranges(_chunks(plain, _TEN_PAGES)) == [(1, _TEN_PAGES)]


def test_page_range_without_pages_is_one_chunk_and_empty_text_is_none() -> None:
    """Test that a document with no page spans is one chunk and blank text has none.

    **Why this test is important:**
      - Only PDFs carry pages; a DOCX or text file routed through the same chunker must
        still be indexed, as one unit without a page range, instead of failing or
        vanishing.

    **What it tests:**
      - Text with no spans gives one chunk of the stripped text with no page range.
      - Whitespace-only text gives no chunks.
    """
    config = ChunkerConfig(kind=ChunkerKind.PAGE_RANGE, unit_pages=2)
    chunker = cast("PageRangeChunker", chunker_from_config(config))

    chunks = chunker.chunk_spans("  some text\n\nmore  ")

    assert [(c.text, c.page_start, c.page_end) for c in chunks] == [
        ("some text\n\nmore", None, None)
    ]
    assert chunker.chunk_spans(" \n\n ", pages=()) == []
    assert chunker.chunk(" \n ") == []


@settings(max_examples=150)
@given(
    st.lists(
        st.text(alphabet="ab #\n", min_size=1, max_size=_MAX_PAGE_CHARS),
        max_size=_MAX_PAGES,
    ),
    st.integers(min_value=1, max_value=_MAX_UNIT_PAGES),
)
def test_page_range_chunks_lose_and_repeat_no_text(
    texts: list[str], unit_pages: int
) -> None:
    """Test that without overlap the chunks together hold exactly the document's text.

    **Why this test is important:**
      - Each chunk becomes a retrievable unit; text dropped between two of them is
        unsearchable and text repeated skews ranking, whatever the pages and headings.

    **What it tests:**
      - For arbitrary pages and any ``unit_pages``, the chunk texts joined equal the
        markdown with whitespace removed, and the chunks' page ranges never run
        backwards.

    Args:
        texts: The generated text of each page.
        unit_pages: The generated pages per chunk.

    """
    document = _document(texts)

    chunks = _chunks(document, unit_pages)

    def squeezed(text: str) -> str:
        return "".join(text.split())

    assert squeezed("".join(c.text for c in chunks)) == squeezed(
        document.markdown_content
    )
    pages = [c.page_start for c in chunks if c.page_start is not None]
    assert pages == sorted(pages)


def test_page_range_overlap_repeats_trailing_pages() -> None:
    """Test that ``overlap_pages`` repeats that many pages at the start of the next chunk.

    **Why this test is important:**
      - An answer that straddles a unit boundary is found in neither unit unless the
        units share the pages around the cut.

    **What it tests:**
      - With 4 pages per chunk and 1 page of overlap, 10 pages chunk as 1-4, 4-7, 7-10.
      - The repeated page's text appears at the end of one chunk and the start of the
        next.
    """
    document = _document([f"page {n}" for n in range(1, _TEN_PAGES + 1)])

    chunks = _chunks(document, _UNIT_PAGES, overlap_pages=1)

    assert _ranges(chunks) == [(1, 4), (4, 7), (7, 10)]
    assert chunks[0].text.endswith("page 4")
    assert chunks[1].text.startswith("page 4")
