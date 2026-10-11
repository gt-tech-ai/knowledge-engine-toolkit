"""Splitter domain DTO — one index unit a ``DocumentSplitter`` produces.

Shared, dependency-free contract (the element of ``DocumentSplitter.split``'s return list); a large
parsed document is split into contiguous ``ChunkPayload`` units, each its own KB index unit. Lives in
this shared library so any deployable programs against the same DTO (like Go's ``go/core`` types).
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class ChunkPayload:
    """One index unit split from a parent document.

    ``text`` is the chunk's content; ``page_start``/``page_end`` are the inclusive 1-based
    source page range (``None`` for non-paginated / non-PDF content); ``chunk_index`` is the
    0-based position within the parent.
    """

    # chunk_index is the 0-based position of this chunk within its parent document.
    chunk_index: int
    """The 0-based position of this chunk within its parent document."""

    # text is the chunk's content (indexed as its own KB unit).
    text: str
    """The chunk's content, indexed as its own KB unit."""

    # page_start / page_end are the inclusive 1-based source page range (None if non-paginated).
    page_start: int | None
    """Inclusive 1-based first source page of this chunk (``None`` if non-paginated)."""
    page_end: int | None
    """Inclusive 1-based last source page of this chunk (``None`` if non-paginated)."""

    @property
    def size_bytes(self) -> int:
        """The chunk content's UTF-8 byte size (what the KB per-file limit is measured against)."""
        return len(self.text.encode("utf-8"))
