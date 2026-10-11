"""Fixed-size chunker backend (deterministic paragraph-pack; the default ``Chunker``)."""

from techai_webutils.clients.chunking.fixed.chunker import FixedChunker, chunk_text

__all__ = ["FixedChunker", "chunk_text"]
