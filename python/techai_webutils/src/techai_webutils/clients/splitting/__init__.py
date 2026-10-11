"""Document-splitter clients (Go Layer-2 adapters behind ``core.interfaces.splitter.DocumentSplitter``).

Config-selected split backends via ``splitter_from_config`` (: builder at top, each backend
its own subpackage). The ``kb_unit`` backend is pure stdlib (no heavy dep).
"""

from techai_webutils.clients.splitting.factory import (
    SplitterConfig,
    SplitterKind,
    splitter_from_config,
)

__all__ = ["SplitterConfig", "SplitterKind", "splitter_from_config"]
