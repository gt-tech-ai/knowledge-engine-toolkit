"""Deterministic no-Bedrock LLM provider for dev/local.

There is no Bedrock LLM locally, so dev runs this stub: it echoes the last user message so
the query rewriter (passthrough) and the streaming generator both produce deterministic,
inspectable output without AWS. Selected by config (``LlmConfig.kind = stub``).
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.llm import (
    LLMProvider,
    LLMResponse,
    StreamUsage,
    text_only,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage

_STUB_MODEL = "stub-llm"
"""Model id the stub provider reports from ``model_name()`` (no real backend)."""


def _last_user_message(messages: list[LLMMessage]) -> str:
    """Return the content of the last user-role message (empty if none)."""
    for message in reversed(messages):
        if message.role == "user":
            return message.content
    return ""


def _input_tokens(messages: list[LLMMessage]) -> int:
    """Count prompt tokens as whitespace-delimited words across every message (deterministic)."""
    return sum(len(m.content.split()) for m in messages)


async def _token_stream(
    text: str, usage: StreamUsage
) -> AsyncIterator[str | StreamUsage]:
    """Yield ``text`` token-by-token (whitespace-delimited, trailing space preserved), then ``usage``.

    Each token first yields to the event loop, like a real network stream, so a consumer that
    cancels mid-stream is exercised the same way it is against a real provider.
    """
    for token in text.split(" "):
        if token:
            await asyncio.sleep(0)
            yield f"{token} "
    yield usage


class StubLlmProvider(NoOpAsyncResource, LLMProvider):
    """LLMProvider that echoes the last user message (deterministic dev behavior)."""

    def __init__(self, model: str = _STUB_MODEL) -> None:
        """Bind the reported model name."""
        self._model = model

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Return the last user message verbatim as the completion."""
        answer = _last_user_message(messages)
        return LLMResponse(
            content=answer,
            model=self._model,
            input_tokens=_input_tokens(messages),
            output_tokens=len(answer.split()),
            finish_reason="stop",
        )

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Return an async iterator streaming the last user message token-by-token."""
        return text_only(await self.stream_with_usage(messages, config))

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Stream the last user message token-by-token, then a word-count ``StreamUsage``."""
        answer = _last_user_message(messages)
        usage = StreamUsage(
            model=self._model,
            input_tokens=_input_tokens(messages),
            output_tokens=len(answer.split()),
            finish_reason="stop",
        )
        return _token_stream(answer, usage)

    @override
    def model_name(self) -> str:
        """Return the stub model identifier."""
        return self._model
