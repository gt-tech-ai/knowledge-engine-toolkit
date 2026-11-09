"""Unit tests for the ``ParsedDocument`` JSON codec (the parse stage's artifact form)."""

from __future__ import annotations

import json

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from techai_webutils.core.domain import (
    PARSED_FORMAT_VERSION,
    BlockType,
    DocumentBlock,
    DocumentFormat,
    Page,
    ParsedDocument,
    join_parsed,
    parsed_from_json,
    parsed_to_json,
)
from techai_webutils.core.errors import AppValueError

_DOCS = st.builds(
    ParsedDocument,
    markdown_content=st.text(),
    document_format=st.sampled_from(DocumentFormat),
    total_pages=st.integers(min_value=0, max_value=10_000),
    word_count=st.integers(min_value=0, max_value=10**9),
    pages=st.lists(st.builds(Page, st.integers(min_value=1), st.text()), max_size=5),
    error=st.text(max_size=40),
    attributes=st.dictionaries(st.text(max_size=8), st.text(max_size=8), max_size=3),
    blocks=st.lists(
        st.builds(
            DocumentBlock,
            st.sampled_from(BlockType),
            st.text(),
            st.integers(min_value=0),
            st.integers(min_value=0),
            st.lists(st.text(max_size=10), max_size=3).map(tuple),
        ),
        max_size=4,
    ).map(tuple),
)
"""Parsed documents with arbitrary text, pages and blocks."""


@settings(max_examples=150)
@given(_DOCS)
def test_parsed_document_round_trips_through_json(doc: ParsedDocument) -> None:
    """A parsed document survives ``parsed_to_json`` → JSON text →.

    ``parsed_from_json``.

    **Why this test is important:**
      - The parse stage's checkpoint reuses this form instead of parsing again; a field
        lost or changed on the way back would index different text than a fresh parse
        would.

    **What it tests:**
      - For any document, encoding, serializing to JSON text, and decoding gives an equal
        document.
    """
    assert parsed_from_json(json.loads(json.dumps(parsed_to_json(doc)))) == doc


def test_parsed_from_json_refuses_unknown_versions_and_malformed_payloads() -> None:
    """Test that the reader refuses what it cannot read instead of guessing.

    **Why this test is important:**
      - A checkpoint written by a newer release, or a damaged one, must read as an error
        the caller can treat as a miss, never as a document with empty text.

    **What it tests:**
      - A ``format_version`` above N or below N-1, a missing required field, and an
        unknown format each raise ``AppValueError``; a payload with no ``blocks`` reads as
        no blocks.
    """
    good = parsed_to_json(ParsedDocument("x", DocumentFormat.TXT))
    for version in (PARSED_FORMAT_VERSION + 1, PARSED_FORMAT_VERSION - 2):
        with pytest.raises(AppValueError):
            parsed_from_json({**good, "format_version": version})
    with pytest.raises(AppValueError):
        parsed_from_json({"format_version": PARSED_FORMAT_VERSION})
    with pytest.raises(AppValueError):
        parsed_from_json({**good, "document_format": "nope"})
    without_blocks = {k: v for k, v in good.items() if k != "blocks"}
    assert parsed_from_json(without_blocks).blocks == ()


def _part(first: int, last: int, *, error: str = "") -> ParsedDocument:
    """Return a PDF part covering pages ``first``..``last``, one block per page.

    Returns:
        A PDF part covering pages ``first``..``last``, one block per page.

    """
    pages = [Page(n, f"page {n}") for n in range(first, last + 1)]
    blocks = tuple(
        DocumentBlock(BlockType.PARAGRAPH, f"page {n}", i, n)
        for i, n in enumerate(range(first, last + 1))
    )
    return ParsedDocument(
        "\n\n".join(p.text for p in pages),
        DocumentFormat.PDF,
        total_pages=len(pages),
        word_count=2 * len(pages),
        pages=pages,
        error=error,
        blocks=blocks,
    )


def test_join_parsed_reassembles_parts_into_one_document() -> None:
    """Test that the parts of a PDF parsed in page ranges join into the whole document.

    **Why this test is important:**
      - A large PDF is parsed in parts so each can be checkpointed; the chunker must then
        see one document whose blocks are in reading order with unique positions, exactly
        as if it had been parsed whole, and a failed part must fail the document rather
        than silently drop pages.

    **What it tests:**
      - Parts given out of order join in page order: markdown, pages and blocks
        concatenate, block ``order`` runs 0..n-1, word counts add and ``total_pages``
        counts every page.
      - A part carrying an error makes the joined document carry it.
      - Joining nothing raises ``AppValueError``.
    """
    joined = join_parsed([_part(4, 5), _part(1, 3)])

    assert joined.ok
    assert joined.document_format is DocumentFormat.PDF
    assert [p.page_number for p in joined.pages] == [1, 2, 3, 4, 5]
    assert [(b.order, b.page) for b in joined.blocks] == [(i, i + 1) for i in range(5)]
    assert joined.markdown_content == "\n\n".join(f"page {n}" for n in range(1, 6))
    assert (joined.word_count, joined.total_pages) == (10, 5)
    assert join_parsed([_part(1, 2), _part(3, 3, error="parse_timeout")]).error == (
        "parse_timeout"
    )
    with pytest.raises(AppValueError):
        join_parsed([])
