"""Chunker stage-collaborator contract: split parsed text into retrieval-sized chunks.

The embedding-granularity chunk seam a ``KnowledgeBase`` indexer applies per index unit (NOT a
standalone stage — an index-stage collaborator). Config-selected backends in
``techai_webutils.clients.chunking`` satisfy it. A structure-aware backend (the ``sentence``
kind) also satisfies ``StructuredChunker``: each chunk keeps its source span, section path,
element type and the entities inside it. Imported by full submodule path (not re-exported from
``core.interfaces``).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.annotator import Entity


@runtime_checkable
class Chunker(Protocol):
    """Split document text into retrieval-sized chunks for embedding (one backend per Kind)."""

    def chunk(self, text: str) -> list[str]:
        """Return the non-empty, size-bounded chunks of ``text`` (deterministic — reproducible indexing)."""
        ...


@dataclass(frozen=True, slots=True)
class StructuredChunk:
    """One chunk with where it came from.

    ``[start, end)`` is the chunk's range of the source text. Consecutive chunks may overlap (the
    configured overlap); with the overlaps removed, the ranges tile the text. ``text`` is what is
    embedded: the source range, except that a table piece after the first repeats the table's
    header rows first.
    """

    text: str
    """The text to embed and index."""
    start: int
    """Offset of the chunk's first source character."""
    end: int
    """Offset one past the chunk's last source character."""
    section_path: tuple[str, ...] = ()
    """The headings the chunk sits under, outermost first."""
    element_type: str = "paragraph"
    """``paragraph``, ``table``, ``code`` or ``heading`` (a section with no body)."""
    caption: str = ""
    """A table chunk's caption; empty otherwise."""
    entities: tuple[Entity, ...] = ()
    """The input entities wholly inside ``[start, end)``, in text order."""


@runtime_checkable
class StructuredChunker(Chunker, Protocol):
    """A chunker that also reports each chunk's span, section and type."""

    def chunk_spans(
        self, text: str, *, entities: Sequence[Entity] = ()
    ) -> list[StructuredChunk]:
        """Return ``text``'s chunks with their spans, sections and types.

        ``entities`` (offsets into ``text``) are attached to the chunks that contain them.
        """
        ...
