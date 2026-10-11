"""KB-unit document-splitter backend (page/paragraph pack to a byte ceiling; the default splitter)."""

from techai_webutils.clients.splitting.kb_unit.splitter import (
    KbUnitSplitter,
    split_into_chunks,
)

__all__ = ["KbUnitSplitter", "split_into_chunks"]
