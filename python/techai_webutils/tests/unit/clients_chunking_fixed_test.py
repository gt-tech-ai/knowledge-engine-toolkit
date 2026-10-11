"""Tests for the fixed chunker backend (``chunk_text``) + the ``chunker_from_config`` factory."""

from __future__ import annotations

import pytest

from techai_webutils.clients.chunking import (
    ChunkerConfig,
    ChunkerKind,
    chunker_from_config,
)
from techai_webutils.clients.chunking.fixed import FixedChunker, chunk_text
from techai_webutils.core.interfaces.chunker import Chunker


class TestChunkText:
    """Behavioural tests for chunk_text (paragraph-pack with hard-split fallback)."""

    def test_splits_into_nonempty_chunks_bounded_by_size(self) -> None:
        """chunk_text produces non-empty chunks, each within the size bound, preserving the text."""
        text = "\n\n".join(f"Paragraph number {i} with some words." for i in range(20))
        chunks = chunk_text(text, max_chars=100)
        assert chunks, "expected at least one chunk"
        assert all(c.strip() for c in chunks), "no empty/whitespace-only chunks"
        assert all(len(c) <= 100 for c in chunks), "each chunk within max_chars"
        assert "Paragraph number 0" in chunks[0]

    def test_empty_text_yields_no_chunks(self) -> None:
        """Whitespace-only content produces no chunks (nothing to index)."""
        assert chunk_text("   \n\n  ", max_chars=100) == []

    def test_hard_splits_a_paragraph_longer_than_the_bound(self) -> None:
        """A single paragraph exceeding max_chars is hard-split into within-bound pieces."""
        text = " ".join(f"word{i}" for i in range(200))
        chunks = chunk_text(text, max_chars=80)
        assert len(chunks) > 1, (
            "an over-long paragraph must be split into multiple chunks"
        )
        assert all(len(c) <= 80 for c in chunks)

    def test_is_deterministic(self) -> None:
        """The same text always yields the same chunks (indexing must be reproducible)."""
        text = "\n\n".join(f"Para {i} content." for i in range(30))
        assert chunk_text(text, max_chars=120) == chunk_text(text, max_chars=120)


class TestFixedChunkerFactory:
    """The ``fixed`` Chunker backend + the config factory (chunking-quality C1 seam)."""

    def test_factory_selects_fixed_backend(self) -> None:
        """kind=fixed builds a ``FixedChunker`` satisfying the ``Chunker`` seam.

        **Why this test is important:**
          - The chunk seam must be reachable through the factory so a better chunker becomes a config
            swap, not a code edit (chunking-quality C1).

        **What it tests:**
          - ``chunker_from_config`` returns a ``FixedChunker`` that is a ``Chunker``.
        """
        chunker = chunker_from_config(ChunkerConfig())
        assert isinstance(chunker, FixedChunker)
        assert isinstance(chunker, Chunker)

    def test_fixed_chunker_delegates_to_chunk_text(self) -> None:
        """FixedChunker.chunk equals ``chunk_text`` at the configured budget (parity with the pure fn)."""
        text = "\n\n".join(f"Paragraph number {i} with some words." for i in range(20))
        chunker = chunker_from_config(
            ChunkerConfig(kind=ChunkerKind.FIXED, max_chars=100)
        )
        assert chunker.chunk(text) == chunk_text(text, max_chars=100)

    def test_factory_rejects_unknown_kind(self) -> None:
        """An unknown chunker kind fails loudly with ValueError (the Go NewFromConfig contract)."""
        with pytest.raises(ValueError, match="unknown chunker kind"):
            chunker_from_config(ChunkerConfig(kind="bogus"))  # type: ignore[arg-type]
