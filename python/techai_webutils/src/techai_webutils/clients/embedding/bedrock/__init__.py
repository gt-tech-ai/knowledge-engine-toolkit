"""Bedrock embedding provider backend.

Titan text embeddings via bedrock-runtime ``InvokeModel``.
"""

from techai_webutils.clients.embedding.bedrock.provider import (
    BedrockEmbeddingProvider,
    BedrockRuntimeInvoker,
)
from techai_webutils.clients.embedding.bedrock.runtime import LazyBedrockRuntime

__all__ = ["BedrockEmbeddingProvider", "BedrockRuntimeInvoker", "LazyBedrockRuntime"]
