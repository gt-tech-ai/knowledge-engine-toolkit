"""OpenAI-compatible LLM backend (``/v1/chat/completions``).

vLLM, LiteLLM, llama.cpp and OpenAI serve it.
"""

from techai_webutils.clients.llm.openai_compatible.provider import (
    OpenAICompatibleLlmProvider,
)

__all__ = ["OpenAICompatibleLlmProvider"]
