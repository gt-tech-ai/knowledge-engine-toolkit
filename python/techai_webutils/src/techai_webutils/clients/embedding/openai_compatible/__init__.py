"""OpenAI-compatible embedding provider backend (``/v1/embeddings``: vLLM, TEI, LiteLLM, OpenAI)."""

from techai_webutils.clients.embedding.openai_compatible.provider import (
    OpenAICompatibleEmbeddingProvider,
)

__all__ = ["OpenAICompatibleEmbeddingProvider"]
