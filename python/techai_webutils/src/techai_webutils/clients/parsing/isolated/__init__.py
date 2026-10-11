"""Isolated document-parser backend (MarkItDown + pymupdf, run in a bounded subprocess).

The default ``parsing`` backend: ``IsolatedParser`` runs each parse in a memory/wall-clock-bounded
child process; ``MarkItDownParser`` is the in-process converter it runs there. The heavy
``markitdown``/``pymupdf`` imports stay lazy (inside the parse methods / the forkserver), so importing
this subpackage does not pull them (heavy-deps-lazy rule).
"""

from techai_webutils.clients.parsing.isolated.detect import detect_format
from techai_webutils.clients.parsing.isolated.isolated import (
    IsolatedParser,
    IsolationError,
    run_isolated,
)
from techai_webutils.clients.parsing.isolated.markitdown_parser import MarkItDownParser

__all__ = [
    "IsolatedParser",
    "IsolationError",
    "MarkItDownParser",
    "detect_format",
    "run_isolated",
]
