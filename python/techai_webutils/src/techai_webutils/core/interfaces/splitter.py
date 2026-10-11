"""DocumentSplitter stage contract: split a large parse into contiguous index units.

The ``split`` stage seam: a large parsed document becomes N contiguous ``ChunkPayload`` index units
(each its own KB unit); the small path is a pass-through (one unit). Config-selected backends in
``techai_webutils.clients.splitting`` satisfy it; imported by full submodule path (not re-exported
from ``core.interfaces``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from techai_webutils.core.domain import ChunkPayload, ParsedDocument


@runtime_checkable
class DocumentSplitter(Protocol):
    """Split a parsed document into contiguous index units (always >= 1; one unit on the small path)."""

    def split(self, parsed: ParsedDocument) -> list[ChunkPayload]:
        """Return the parse's index units — contiguous, non-overlapping, each within the size budget."""
        ...
