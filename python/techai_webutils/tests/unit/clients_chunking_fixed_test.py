"""Tests for the fixed chunker backend.

(``chunk_text``) + the ``chunker_from_config`` factory.
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.chunking import (
    ChunkerConfig,
    ChunkerKind,
    chunker_from_config,
)
from techai_webutils.clients.chunking.fixed import FixedChunker, chunk_text
from techai_webutils.core.interfaces.chunker import Chunker

_EXPECTED_C_COUNT = 100
_EXPECTED_C_COUNT_80 = 80


class TestChunkText:
    """Behavioural tests for chunk_text (paragraph-pack with hard-split fallback)."""

    @staticmethod
    def test_splits_into_nonempty_chunks_bounded_by_size() -> None:
        """chunk_text produces non-empty chunks, each within the size bound.

        Preserving the text.
        """
        text = "\n\n".join(f"Paragraph number {i} with some words." for i in range(20))
        chunks = chunk_text(text, max_chars=100)
        assert chunks, "expected at least one chunk"
        assert all(c.strip() for c in chunks), "no empty/whitespace-only chunks"
        assert all(len(c) <= _EXPECTED_C_COUNT for c in chunks), (
            "each chunk within max_chars"
        )
        assert "Paragraph number 0" in chunks[0]

    @staticmethod
    def test_empty_text_yields_no_chunks() -> None:
        """Whitespace-only content produces no chunks (nothing to index)."""
        assert chunk_text("   \n\n  ", max_chars=100) == []

    @staticmethod
    def test_hard_splits_a_paragraph_longer_than_the_bound() -> None:
        """A single paragraph exceeding max_chars is hard-split.

        Into within-bound pieces.
        """
        text = " ".join(f"word{i}" for i in range(200))
        chunks = chunk_text(text, max_chars=80)
        assert len(chunks) > 1, (
            "an over-long paragraph must be split into multiple chunks"
        )
        assert all(len(c) <= _EXPECTED_C_COUNT_80 for c in chunks)

    @staticmethod
    def test_is_deterministic() -> None:
        """The same text always yields the same chunks (indexing must be reproducible)."""
        text = "\n\n".join(f"Para {i} content." for i in range(30))
        assert chunk_text(text, max_chars=120) == chunk_text(text, max_chars=120)


class TestFixedChunkerFactory:
    """The ``fixed`` Chunker backend + the config factory (chunking-quality C1 seam)."""

    @staticmethod
    def test_factory_selects_fixed_backend() -> None:
        """kind=fixed builds a ``FixedChunker`` satisfying the ``Chunker`` seam.

        **Why this test is important:**
          - The chunk seam must be reachable through the factory so a better chunker
            becomes a config swap, not a code edit (chunking-quality C1).

        **What it tests:**
          - ``chunker_from_config`` returns a ``FixedChunker`` that is a ``Chunker``.
        """
        chunker = chunker_from_config(ChunkerConfig())
        assert isinstance(chunker, FixedChunker)
        assert isinstance(chunker, Chunker)

    @staticmethod
    def test_fixed_chunker_delegates_to_chunk_text() -> None:
        """FixedChunker.chunk equals ``chunk_text`` at the configured budget.

        (parity with the pure fn).
        """
        text = "\n\n".join(f"Paragraph number {i} with some words." for i in range(20))
        chunker = chunker_from_config(
            ChunkerConfig(kind=ChunkerKind.FIXED, max_chars=100)
        )
        assert chunker.chunk(text) == chunk_text(text, max_chars=100)

    @staticmethod
    def test_factory_rejects_unknown_kind() -> None:
        """An unknown chunker kind fails loudly with ValueError.

        (the Go NewFromConfig contract).
        """
        with pytest.raises(ValueError, match="unknown chunker kind"):
            chunker_from_config(ChunkerConfig(kind="bogus"))  # type: ignore[arg-type]
