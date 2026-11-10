"""KB-unit document-splitter backend (the default splitter).

It packs pages and paragraphs up to a byte ceiling.
"""

from techai_webutils.clients.splitting.kb_unit.splitter import (
    KbUnitSplitter,
    split_into_chunks,
)

__all__ = ["KbUnitSplitter", "split_into_chunks"]
