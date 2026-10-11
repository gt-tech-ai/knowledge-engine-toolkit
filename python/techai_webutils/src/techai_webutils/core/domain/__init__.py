"""Core domain DTOs shared across deployables (the document-processing stage contracts).

The dependency-free value objects a shared stage ABC references in its signature — parse
(``DocumentFormat``, ``Page``, ``ParsedDocument``), metadata (``DocumentMetadata``), and split
(``ChunkPayload``). A DTO lives here only when a ``core.interfaces`` stage ABC references it, so an
app programs against the same contract regardless of where the concretion runs (like Go's
``go/core``). App-internal DTOs (request args, events) stay in the app's own ``core``.
"""

from techai_webutils.core.domain.metadata import DocumentMetadata
from techai_webutils.core.domain.parser import DocumentFormat, Page, ParsedDocument
from techai_webutils.core.domain.splitter import ChunkPayload

__all__ = [
    "ChunkPayload",
    "DocumentFormat",
    "DocumentMetadata",
    "Page",
    "ParsedDocument",
]
