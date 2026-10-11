"""Tests for the kb_unit document splitter + the ``splitter_from_config`` factory.

Why these tests matter: the splitter turns one large parse into N index units. A wrong
page range breaks citation provenance, an over-max chunk breaks the KB per-file limit,
and a missing "always >=1 chunk" invariant would silently drop a document.
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.splitting import (
    SplitterConfig,
    SplitterKind,
    splitter_from_config,
)
from techai_webutils.clients.splitting.kb_unit import KbUnitSplitter, split_into_chunks
from techai_webutils.core.domain import ChunkPayload, DocumentFormat, Page, ParsedDocument
from techai_webutils.core.interfaces.splitter import DocumentSplitter


def _page(number: int, size: int) -> Page:
    """Return a page whose UTF-8 text is exactly ``size`` bytes."""
    return Page(page_number=number, text="x" * size)


def _pdf(pages: list[Page]) -> ParsedDocument:
    return ParsedDocument(
        markdown_content="",
        document_format=DocumentFormat.PDF,
        total_pages=len(pages),
        pages=pages,
    )


def test_pages_split_into_contiguous_ranges_each_within_max() -> None:
    """Four ~100-byte pages with max 250 pack two per chunk, contiguous and non-overlapping."""
    chunks = split_into_chunks(
        _pdf([_page(1, 100), _page(2, 100), _page(3, 100), _page(4, 100)]), max_bytes=250
    )

    assert [(c.page_start, c.page_end) for c in chunks] == [(1, 2), (3, 4)]
    assert [c.chunk_index for c in chunks] == [0, 1]
    for chunk in chunks:
        assert chunk.size_bytes <= 250


def test_content_within_ceiling_is_a_single_chunk() -> None:
    """When the whole document fits under the ceiling, exactly one chunk spans all pages."""
    chunks = split_into_chunks(_pdf([_page(1, 50), _page(2, 50)]), max_bytes=10_000)

    assert len(chunks) == 1
    assert (chunks[0].page_start, chunks[0].page_end) == (1, 2)


def test_non_paginated_markdown_splits_by_paragraph_without_page_range() -> None:
    """A non-PDF document splits its markdown by paragraph and carries no page range."""
    markdown = "\n\n".join(["p" * 100, "q" * 100, "r" * 100])
    chunks = split_into_chunks(
        ParsedDocument(markdown_content=markdown, document_format=DocumentFormat.TXT),
        max_bytes=250,
    )

    assert len(chunks) >= 2
    assert all(c.page_start is None and c.page_end is None for c in chunks)
    for chunk in chunks:
        assert chunk.size_bytes <= 250


def test_single_page_over_ceiling_is_emitted_alone() -> None:
    """A page bigger than the ceiling is emitted as its own (over-max) chunk, not merged."""
    chunks = split_into_chunks(_pdf([_page(1, 500), _page(2, 50)]), max_bytes=100)

    assert (chunks[0].page_start, chunks[0].page_end) == (1, 1)
    assert (chunks[1].page_start, chunks[1].page_end) == (2, 2)
    assert len(chunks) == 2


def test_empty_document_yields_one_chunk() -> None:
    """An empty parse still yields exactly one chunk (never zero — the document is not dropped)."""
    chunks = split_into_chunks(
        ParsedDocument(markdown_content="", document_format=DocumentFormat.TXT),
        max_bytes=100,
    )

    assert len(chunks) == 1
    assert isinstance(chunks[0], ChunkPayload)


class TestKbUnitSplitterFactory:
    """The ``kb_unit`` DocumentSplitter backend + the config factory (the split stage seam)."""

    def test_factory_selects_kb_unit_backend(self) -> None:
        """kind=kb_unit builds a ``KbUnitSplitter`` satisfying the ``DocumentSplitter`` seam.

        **Why this test is important:**
          - The split stage must be reachable through the factory so a layout-aware splitter becomes a
            config swap, not a code edit.

        **What it tests:**
          - ``splitter_from_config`` returns a ``KbUnitSplitter`` that is a ``DocumentSplitter``.
        """
        splitter = splitter_from_config(
            SplitterConfig(kind=SplitterKind.KB_UNIT, max_bytes=250)
        )
        assert isinstance(splitter, KbUnitSplitter)
        assert isinstance(splitter, DocumentSplitter)

    def test_kb_unit_splitter_delegates_to_split_into_chunks(self) -> None:
        """KbUnitSplitter.split equals ``split_into_chunks`` at the configured ceiling (byte-parity)."""
        parsed = _pdf([_page(1, 100), _page(2, 100), _page(3, 100), _page(4, 100)])
        splitter = splitter_from_config(SplitterConfig(max_bytes=250))
        assert splitter.split(parsed) == split_into_chunks(parsed, max_bytes=250)

    def test_factory_rejects_unknown_kind(self) -> None:
        """An unknown splitter kind fails loudly with ValueError (the Go NewFromConfig contract)."""
        with pytest.raises(ValueError, match="unknown splitter kind"):
            splitter_from_config(SplitterConfig(kind="bogus"))  # type: ignore[arg-type]
