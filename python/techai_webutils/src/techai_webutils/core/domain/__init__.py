"""Core domain DTOs shared across deployables (the document-processing stage contracts).

The dependency-free value objects a shared stage ABC references in its signature — parse
(``DocumentFormat``, ``Page``, ``ParsedDocument`` and its typed ``DocumentBlock``s), metadata (``DocumentMetadata``), and split
(``ChunkPayload``). A DTO lives here only when a ``core.interfaces`` stage ABC references it, so an
app programs against the same contract regardless of where the concretion runs (like Go's
``go/core``). App-internal DTOs (request args, events) stay in the app's own ``core``.
"""

from techai_webutils.core.domain.document_ir import (
    BlockType,
    DocumentBlock,
    TableCell,
    Turn,
    blocks_from_json,
    blocks_to_json,
)
from techai_webutils.core.domain.metadata import DocumentMetadata
from techai_webutils.core.domain.parser import (
    PARSED_FORMAT_VERSION,
    DocumentFormat,
    Page,
    ParsedDocument,
    parsed_from_json,
    parsed_to_json,
)
from techai_webutils.core.domain.splitter import ChunkPayload

__all__ = [
    "PARSED_FORMAT_VERSION",
    "BlockType",
    "ChunkPayload",
    "DocumentBlock",
    "DocumentFormat",
    "DocumentMetadata",
    "Page",
    "ParsedDocument",
    "TableCell",
    "Turn",
    "blocks_from_json",
    "blocks_to_json",
    "parsed_from_json",
    "parsed_to_json",
]
