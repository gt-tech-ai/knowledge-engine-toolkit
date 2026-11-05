"""OpenAI-compatible LLM provider backend (``/v1/chat/completions``: vLLM, LiteLLM, llama.cpp, OpenAI)."""

from techai_webutils.clients.llm.openai_compatible.provider import (
    OpenAICompatibleLlmProvider,
)

__all__ = ["OpenAICompatibleLlmProvider"]
