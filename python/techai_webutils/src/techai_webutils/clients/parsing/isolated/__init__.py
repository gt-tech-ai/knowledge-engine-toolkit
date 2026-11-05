"""Isolated document-parser backend (MarkItDown + pymupdf, run in a bounded subprocess).

The default ``parsing`` backend: ``IsolatedParser`` runs each parse in a memory/wall-clock-bounded
child process; ``MarkItDownParser`` is the in-process converter it runs there. The heavy
``markitdown``/``pymupdf`` imports stay lazy (inside the parse methods / the forkserver), so importing
this subpackage does not pull them (heavy-deps-lazy rule).
"""

from techai_webutils.clients.parsing.isolated.detect import detect_format
from techai_webutils.clients.parsing.isolated.isolated import (
    SPAWN_CONTEXT,
    IsolatedParser,
    IsolationError,
    IsolationMemoryError,
    isolation_error_tag,
    run_isolated,
)
from techai_webutils.clients.parsing.isolated.markitdown_parser import MarkItDownParser

__all__ = [
    "SPAWN_CONTEXT",
    "IsolatedParser",
    "IsolationError",
    "IsolationMemoryError",
    "MarkItDownParser",
    "detect_format",
    "isolation_error_tag",
    "run_isolated",
]
