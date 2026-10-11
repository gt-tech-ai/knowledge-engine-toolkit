"""Document metadata domain DTO — the fields a ``MetadataExtractor`` produces.

Shared, dependency-free contract (the return type of
``techai_webutils.core.interfaces.metadata.MetadataExtractor``); an indexer writes these fields
alongside each document (e.g. a knowledge-base metadata sidecar). Lives in this shared library so any
deployable programs against the same DTO (like Go's ``go/core`` types).
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class DocumentMetadata:
    """Extracted metadata written into the Bedrock KB sidecar."""

    document_format: str
    """Detected document format as the ``DocumentFormat`` string value (e.g. ``"pdf"``); stored as a
    plain ``str`` because the sidecar is serialized to JSON, not the enum."""
    page_count: int
    """Number of pages (PDF page count; ``0`` for non-paginated formats)."""
    word_count: int
    """Whitespace-delimited word count over the extracted Markdown content."""
    language: str
    """Dominant language as an ISO 639-1 code (e.g. ``"en"``), or ``"und"`` when undetectable."""
    extracted_at: str
    """Extraction timestamp as an ISO-8601 UTC string, injected by the caller's clock (not read here)."""
