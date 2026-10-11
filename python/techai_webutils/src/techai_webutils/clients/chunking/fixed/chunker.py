"""Deterministic fixed-size text chunking for indexing (the ``fixed`` Chunker backend).

Splits document text into retrieval-sized pieces on paragraph boundaries, packing paragraphs
together up to a size bound and hard-splitting any single paragraph that exceeds it. Deterministic —
the same text always yields the same chunks — so re-indexing a document is reproducible and its
vector-store point ids (``{document_id}:{index}``) stay stable.
"""

from __future__ import annotations

# Default chunk size (characters). Chosen to mirror a Bedrock KB's FIXED_SIZE chunking at
# chunk_max_tokens = 512, so the local vector path indexes at the same granularity as a managed
# knowledge base using that setting — a smaller size (e.g. 800 chars, ~166 tokens) over-chunks ~3x,
# which multiplies local embed cost (one vector per chunk) and makes local retrieval a poor proxy.
# ~2048 chars ≈ 512 tokens (~4 chars/token) and stays under nomic-embed-text's 2048-token context.
# A managed knowledge base that chunks server-side never calls chunk_text. The caller may override
# per document.
_DEFAULT_MAX_CHARS = 2048
"""Default chunk size (chars) mirroring a Bedrock KB's ~512-token FIXED_SIZE chunking."""


def chunk_text(text: str, max_chars: int = _DEFAULT_MAX_CHARS) -> list[str]:
    """Split ``text`` into non-empty chunks no larger than ``max_chars``, on paragraph boundaries.

    Paragraphs (blank-line separated) are packed together up to the size bound; a paragraph longer than
    the bound is hard-split. Deterministic — the same text always yields the same chunks.
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
    """Split an over-long paragraph into <= max_chars pieces on word boundaries (fallback: hard cut)."""
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


class FixedChunker:
    """The ``fixed`` ``Chunker`` backend — paragraph-pack chunking to a fixed character budget."""

    def __init__(self, max_chars: int = _DEFAULT_MAX_CHARS) -> None:
        """Bind the per-chunk character budget (defaults to the prod-KB-mirroring ~512-token size)."""
        self._max_chars = max_chars

    def chunk(self, text: str) -> list[str]:
        """Return the deterministic, size-bounded chunks of ``text`` (delegates to ``chunk_text``)."""
        return chunk_text(text, self._max_chars)
