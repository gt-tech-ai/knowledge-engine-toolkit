"""MetadataExtractor stage contract: derive ``DocumentMetadata`` from a parsed document.

The ``metadata`` stage seam: format / page & word counts / detected language, stamped with
a caller-supplied timestamp. Config-selected backends in
``techai_webutils.clients.metadata`` satisfy it; imported by full submodule path (not
re-exported from ``core.interfaces``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from techai_webutils.core.domain import DocumentMetadata, ParsedDocument


@runtime_checkable
class MetadataExtractor(Protocol):
    """Extract ``DocumentMetadata`` (format, counts, language) from a parsed document."""

    def extract(self, parsed: ParsedDocument, *, extracted_at: str) -> DocumentMetadata:
        """Return the metadata for ``parsed``.

        The caller supplies the ``extracted_at`` timestamp.

        Args:
            parsed: The parsed document.
            extracted_at: The extraction time, as an ISO-8601 string stamped on the
                result.

        Returns:
            The document's metadata (title, language, counts).

        """
        ...
