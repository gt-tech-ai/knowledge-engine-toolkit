"""Document-parser clients (Go Layer-2 adapters behind ``core.interfaces.parser.DocumentParser``).

Config-selected parse backends via ``parser_from_config`` (: builder at top, each backend its
own subpackage). The ``isolated`` backend (MarkItDown + pymupdf in a bounded subprocess) keeps its
heavy deps lazy, so importing this package pulls neither ``markitdown`` nor ``pymupdf``.
"""

from techai_webutils.clients.parsing.factory import (
    ParserConfig,
    ParserKind,
    parser_from_config,
)

__all__ = ["ParserConfig", "ParserKind", "parser_from_config"]
