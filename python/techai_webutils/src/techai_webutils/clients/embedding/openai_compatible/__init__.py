"""OpenAI-compatible embedding backend (``/v1/embeddings``).

vLLM, TEI, LiteLLM and OpenAI serve it.
"""

from techai_webutils.clients.embedding.openai_compatible.provider import (
    OpenAICompatibleEmbeddingProvider,
)

__all__ = ["OpenAICompatibleEmbeddingProvider"]
