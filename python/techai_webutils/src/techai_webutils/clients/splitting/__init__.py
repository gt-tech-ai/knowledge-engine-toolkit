"""Document-splitter clients.

Go's Layer-2 adapters behind ``core.interfaces.splitter.DocumentSplitter``.

Config-selected split backends via ``splitter_from_config`` (the factory here, each
backend its own subpackage). ``kb_unit`` is pure stdlib, ``markdown`` needs only PyYAML,
and ``code`` needs the ``code`` extra (tree-sitter and its grammars).
"""

from techai_webutils.clients.splitting.factory import (
    SplitterConfig,
    SplitterKind,
    splitter_from_config,
)

__all__ = ["SplitterConfig", "SplitterKind", "splitter_from_config"]
