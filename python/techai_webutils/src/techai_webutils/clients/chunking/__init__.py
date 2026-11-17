"""Chunker clients (Go Layer-2 adapters behind ``core.interfaces.chunker.Chunker``).

Config-selected embedding-granularity chunk backends via ``chunker_from_config`` (the
builder at top, each backend its own subpackage). The ``fixed`` backend is pure stdlib (no
heavy dep); the ``sentence`` backend takes an injected ``SentenceSplitter`` and tokenizer;
the ``page_range`` backend cuts a paged document into runs of pages, each chunk carrying
its page range.
``context`` holds the chunk contextualizers (``contextualizer_from_config``).
"""

from techai_webutils.clients.chunking.factory import (
    ChunkerConfig,
    ChunkerKind,
    chunker_from_config,
)

__all__ = ["ChunkerConfig", "ChunkerKind", "chunker_from_config"]
