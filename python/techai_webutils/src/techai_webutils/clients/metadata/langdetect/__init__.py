"""Langdetect metadata-extractor backend (the default ``MetadataExtractor``).

Importing this subpackage loads ``langdetect`` (the optional ``[langdetect]`` extra) and warms its
language profiles; the factory imports it lazily, so the ``clients.metadata`` package stays
langdetect-free until this backend is selected.
"""

from techai_webutils.clients.metadata.langdetect.extractor import (
    LangdetectMetadataExtractor,
    extract_metadata,
)

__all__ = ["LangdetectMetadataExtractor", "extract_metadata"]
