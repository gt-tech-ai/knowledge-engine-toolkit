"""The ``code`` document-splitter backend: one index unit per top-level declaration.

Needs the ``code`` extra (tree-sitter and the Go, Python and TypeScript grammars); the
factory turns its absence into a coded error.
"""

from techai_webutils.clients.splitting.code.splitter import CodeSplitter

__all__ = ["CodeSplitter"]
