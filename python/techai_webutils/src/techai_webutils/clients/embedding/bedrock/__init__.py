"""Bedrock embedding backend: Titan text embeddings via ``InvokeModel``.

The calls go to bedrock-runtime.
"""

from techai_webutils.clients.embedding.bedrock.provider import (
    BedrockEmbeddingProvider,
    BedrockRuntimeInvoker,
)
from techai_webutils.clients.embedding.bedrock.runtime import LazyBedrockRuntime

__all__ = ["BedrockEmbeddingProvider", "BedrockRuntimeInvoker", "LazyBedrockRuntime"]
