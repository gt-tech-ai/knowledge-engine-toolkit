"""Embedding provider clients (Go Layer-2 adapters behind ``core.interfaces.embedding``).

Text→vector providers selected by config via ``new_embedding_from_config`` (Ollama for local dev). Package
shape: builder at top, each backend its own subpackage.
"""

from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)

__all__ = ["EmbeddingConfig", "EmbeddingKind", "new_embedding_from_config"]
