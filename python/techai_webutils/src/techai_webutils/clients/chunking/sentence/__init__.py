"""The ``sentence`` chunker: token-sized chunks at sentence boundaries.

Tables are kept whole.
"""

from techai_webutils.clients.chunking.sentence.blocks import (
    Block,
    BlockKind,
    split_blocks,
)
from techai_webutils.clients.chunking.sentence.chunker import SentenceChunker

__all__ = ["Block", "BlockKind", "SentenceChunker", "split_blocks"]
