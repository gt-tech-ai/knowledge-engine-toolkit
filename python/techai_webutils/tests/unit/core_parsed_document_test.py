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
    """Test that a parsed document survives ``parsed_to_json`` → JSON text → ``parsed_from_json``.

    **Why this test is important:**
      - The parse stage's checkpoint reuses this form instead of parsing again; a field lost or
        changed on the way back would index different text than a fresh parse would.

    **What it tests:**
      - For any document, encoding, serializing to JSON text, and decoding gives an equal
        document.
    """
    assert parsed_from_json(json.loads(json.dumps(parsed_to_json(doc)))) == doc


def test_parsed_from_json_refuses_unknown_versions_and_malformed_payloads() -> None:
    """Test that the reader refuses what it cannot read instead of guessing.

    **Why this test is important:**
      - A checkpoint written by a newer release, or a damaged one, must read as an error the
        caller can treat as a miss, never as a document with empty text.

    **What it tests:**
      - A ``format_version`` above N or below N-1, a missing required field, and an unknown
        format each raise ``AppValueError``; a payload with no ``blocks`` reads as no blocks.
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
