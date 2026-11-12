"""Splitter domain DTO — one index unit a ``DocumentSplitter`` produces.

Shared, dependency-free contract (the element of ``DocumentSplitter.split``'s return
list); a large parsed document is split into contiguous ``ChunkPayload`` units, each its
own KB index unit. Lives in this shared library so any deployable programs against the
same DTO (like Go's ``go/core`` types).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Mapping


@dataclass(frozen=True, slots=True)
class ChunkPayload:
    """One index unit split from a parent document.

    ``text`` is the chunk's content; ``page_start``/``page_end`` are the inclusive 1-based
    source page range (``None`` for non-paginated / non-PDF content); ``chunk_index`` is
    the 0-based position within the parent. ``section_path``, ``element_type`` and
    ``span`` say where the chunk came from when a structure-aware chunker made it (empty /
    ``None`` otherwise), so a sink can carry them into the vector store's metadata and the
    sidecar. ``symbol``, ``line_start``/``line_end`` and ``frontmatter`` are set by the
    ``code`` and ``markdown`` splitters: the declaration or section the chunk is, its
    1-based line range in the source file, and the document's parsed frontmatter.
    """

    # chunk_index is the 0-based position of this chunk within its parent document.
    chunk_index: int
    """The 0-based position of this chunk within its parent document."""

    # text is the chunk's content (indexed as its own KB unit).
    text: str
    """The chunk's content, indexed as its own KB unit."""

    # page_start / page_end are the inclusive 1-based source page range (None if
    # non-paginated).
    page_start: int | None
    """Inclusive 1-based first source page of this chunk (``None`` if non-paginated)."""
    page_end: int | None
    """Inclusive 1-based last source page of this chunk (``None`` if non-paginated)."""

    # section_path / element_type / span locate the chunk in its document's structure.
    section_path: tuple[str, ...] = ()
    """The headings the chunk sits under, outermost first (empty when unknown)."""
    element_type: str = ""
    """``paragraph``, ``table``, ``code``, ``heading`` ... (empty when unknown)."""
    span: tuple[int, int] | None = None
    """The chunk's ``[start, end)`` character range in the parent's text (``None`` when
    unknown)."""

    symbol: str = ""
    """The declaration (``Type.method``) or ``##`` section the chunk is; empty for glue
    (imports, a preamble) and for splitters that do not know it."""
    line_start: int | None = None
    """Inclusive 1-based first source line of the chunk (``None`` when unknown)."""
    line_end: int | None = None
    """Inclusive 1-based last source line of the chunk (``None`` when unknown)."""
    frontmatter: Mapping[str, object] = field(default_factory=dict[str, object])
    """The source document's parsed YAML frontmatter, the same on every chunk of it
    (empty when the document has none)."""

    @property
    def size_bytes(self) -> int:
        """The chunk content's UTF-8 byte size.

        The KB per-file limit is measured against it.
        """
        return len(self.text.encode("utf-8"))
