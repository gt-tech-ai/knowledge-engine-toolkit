"""Metadata-extractor clients (Go Layer-2 adapters behind ``core.interfaces.metadata.MetadataExtractor``).

Config-selected metadata backends via ``metadata_extractor_from_config`` (: builder at top,
each backend its own subpackage). The ``langdetect`` backend keeps its heavy dep lazy, so importing
this package pulls no ``langdetect``.
"""

from techai_webutils.clients.metadata.factory import (
    MetadataConfig,
    MetadataKind,
    metadata_extractor_from_config,
)

__all__ = ["MetadataConfig", "MetadataKind", "metadata_extractor_from_config"]
