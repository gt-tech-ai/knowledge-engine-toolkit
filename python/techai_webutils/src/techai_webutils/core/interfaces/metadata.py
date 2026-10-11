"""MetadataExtractor stage contract: derive ``DocumentMetadata`` from a parsed document.

The ``metadata`` stage seam: format / page & word counts / detected language, stamped with a
caller-supplied timestamp. Config-selected backends in ``techai_webutils.clients.metadata`` satisfy
it; imported by full submodule path (not re-exported from ``core.interfaces``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from techai_webutils.core.domain import DocumentMetadata, ParsedDocument


@runtime_checkable
class MetadataExtractor(Protocol):
    """Extract ``DocumentMetadata`` (format, counts, language) from a parsed document."""

    def extract(self, parsed: ParsedDocument, *, extracted_at: str) -> DocumentMetadata:
        """Return the metadata for ``parsed``, stamping the caller-supplied ``extracted_at`` timestamp."""
        ...
