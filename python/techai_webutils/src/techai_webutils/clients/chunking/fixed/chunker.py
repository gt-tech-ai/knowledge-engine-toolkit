"""Deterministic fixed-size text chunking for indexing (the ``fixed`` Chunker backend).

Splits document text into retrieval-sized pieces on paragraph boundaries, packing
paragraphs together up to a size bound and hard-splitting any single paragraph that
exceeds it. Deterministic — the same text always yields the same chunks — so re-indexing a
document is reproducible and its vector-store point ids (``{document_id}:{index}``) stay
stable.
"""

from __future__ import annotations

from typing import final

# Default chunk size (characters). Chosen to mirror a Bedrock KB's FIXED_SIZE chunking at
# chunk_max_tokens = 512, so the local vector path indexes at the same granularity as a
# managed knowledge base using that setting — a smaller size (e.g. 800 chars, ~166 tokens)
# over-chunks ~3x, which multiplies local embed cost (one vector per chunk) and makes
# local retrieval a poor proxy. ~2048 chars ≈ 512 tokens (~4 chars/token) and stays under
# nomic-embed-text's 2048-token context. A managed knowledge base that chunks server-side
# never calls chunk_text. The caller may override per document.
_DEFAULT_MAX_CHARS = 2048
"""Default chunk size (chars) mirroring a Bedrock KB's ~512-token FIXED_SIZE chunking."""


def chunk_text(text: str, max_chars: int = _DEFAULT_MAX_CHARS) -> list[str]:
    """Split ``text`` into non-empty chunks of at most ``max_chars``.

    Chunks break on paragraph boundaries.

    Paragraphs (blank-line separated) are packed together up to the size bound; a
    paragraph longer than the bound is hard-split. Deterministic — the same text always
    yields the same chunks.

    Args:
        text: The text to split.
        max_chars: The most characters per chunk.

    Returns:
        The non-empty chunks, in order.

    """
    paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks: list[str] = []
    current = ""
    for paragraph in paragraphs:
        for piece in _hard_split(paragraph, max_chars):
            if not current:
                current = piece
            elif len(current) + 2 + len(piece) <= max_chars:
                current = f"{current}\n\n{piece}"
            else:
                chunks.append(current)
                current = piece
    if current:
        chunks.append(current)
    return chunks


def _hard_split(paragraph: str, max_chars: int) -> list[str]:
    """Split an over-long paragraph into pieces of at most ``max_chars``.

    Pieces break on word boundaries, falling back to a hard cut.

    Args:
        paragraph: The paragraph longer than ``max_chars``.
        max_chars: The most characters per piece.

    Returns:
        The pieces, each within ``max_chars`` unless a single word exceeds it.

    """
    if len(paragraph) <= max_chars:
        return [paragraph]
    pieces: list[str] = []
    remaining = paragraph
    while len(remaining) > max_chars:
        cut = remaining.rfind(" ", 0, max_chars)
        if cut <= 0:
            cut = max_chars
        pieces.append(remaining[:cut].strip())
        remaining = remaining[cut:].strip()
    if remaining:
        pieces.append(remaining)
    return [p for p in pieces if p]


@final
class FixedChunker:
    """The ``fixed`` ``Chunker`` backend: paragraph-packed, fixed-size chunks.

    The budget is in characters.
    """

    def __init__(self, max_chars: int = _DEFAULT_MAX_CHARS) -> None:
        """Bind the per-chunk character budget.

        The default mirrors the production knowledge base's ~512-token size.

        Args:
            max_chars: The most characters per chunk.

        """
        self._max_chars = max_chars

    def chunk(self, text: str) -> list[str]:
        """Return the deterministic, size-bounded chunks of ``text``.

        It delegates to ``chunk_text``.

        Args:
            text: The document text to split.

        Returns:
            The deterministic, size-bounded chunks of ``text``.

        """
        return chunk_text(text, self._max_chars)
