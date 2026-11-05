"""The ``layout`` document-parser backend: PDFs through Docling into typed blocks, bounded.

Importing this subpackage pulls ``docling-core`` (the block mapping's types); full Docling is
imported only inside the parse child.
"""

from techai_webutils.clients.parsing.layout.mapping import parsed_from_docling
from techai_webutils.clients.parsing.layout.parser import (
    LayoutOptions,
    LayoutParser,
    convert_with_docling,
)

__all__ = ["LayoutOptions", "LayoutParser", "convert_with_docling", "parsed_from_docling"]
