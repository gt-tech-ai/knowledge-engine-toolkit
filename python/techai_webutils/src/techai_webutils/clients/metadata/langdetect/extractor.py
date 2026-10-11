"""Metadata extraction via langdetect (the ``langdetect`` MetadataExtractor backend).

``extract_metadata`` is the pure, unit-testable core; ``LangdetectMetadataExtractor`` is the
``MetadataExtractor`` seam over it. Language detection uses ``langdetect`` with a fixed seed for
deterministic results; empty/undetectable text yields ``"und"``.

``langdetect`` is imported at this module's top — an optional ``[langdetect]`` extra — so it loads only
when the ``langdetect`` backend is selected (the factory lazy-imports this subpackage); the
``clients.metadata`` package import stays langdetect-free.
"""

from __future__ import annotations

import contextlib
from typing import TYPE_CHECKING, override

from langdetect import DetectorFactory, LangDetectException, detect

from techai_webutils.core.domain import DocumentMetadata
from techai_webutils.core.interfaces.metadata import MetadataExtractor

if TYPE_CHECKING:
    from techai_webutils.core.domain import ParsedDocument

# Deterministic language detection (langdetect is randomised by default).
DetectorFactory.seed = 0

_UNDETERMINED = "und"
"""Language code recorded when detection is inconclusive (ISO 639-2 'und' = undetermined)."""

# Pre-load langdetect's language profiles at import (single-threaded). ``extract_metadata`` runs under
# ``asyncio.to_thread`` across the batch fan-out, so the first concurrent batch would otherwise race
# langdetect's unguarded lazy ``init_factory()``. One warm ``detect`` forces the one-time profile load
# here, deterministically, before any worker thread calls it — and this module is imported by the
# factory at the (single-threaded) composition root, before the fan-out starts.
with contextlib.suppress(
    LangDetectException
):  # warmup text is always detectable; defensive only
    detect("The quick brown fox jumps over the lazy dog.")


def _detect_language(text: str) -> str:
    """Detect the dominant language of ``text`` (ISO 639-1), or ``"und"`` if undetectable."""
    stripped = text.strip()
    if not stripped:
        return _UNDETERMINED
    try:
        return detect(stripped)
    except LangDetectException:
        return _UNDETERMINED


def extract_metadata(parsed: ParsedDocument, *, extracted_at: str) -> DocumentMetadata:
    """Build a ``DocumentMetadata`` from a parsed document and an extraction timestamp (pure)."""
    return DocumentMetadata(
        document_format=str(parsed.document_format),
        page_count=parsed.total_pages,
        word_count=parsed.word_count,
        language=_detect_language(parsed.markdown_content),
        extracted_at=extracted_at,
    )


class LangdetectMetadataExtractor(MetadataExtractor):
    """The ``langdetect`` ``MetadataExtractor`` backend — format/counts + langdetect language detection."""

    @override
    def extract(self, parsed: ParsedDocument, *, extracted_at: str) -> DocumentMetadata:
        """Return the metadata for ``parsed`` with ``extracted_at`` stamped (via ``extract_metadata``)."""
        return extract_metadata(parsed, extracted_at=extracted_at)
