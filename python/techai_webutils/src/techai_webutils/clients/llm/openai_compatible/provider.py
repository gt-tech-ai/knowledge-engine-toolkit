"""OpenAI-compatible LLM provider — any server speaking ``POST /v1/chat/completions``.

vLLM, LiteLLM, llama.cpp's server and OpenAI accept ``{"model", "messages", "temperature",
"max_tokens", "top_p", "stop", "stream"}``. A non-streaming call returns ``{"choices":
[{"message": {"content"}, "finish_reason"}], "usage": {"prompt_tokens",
"completion_tokens"}}``; a streaming one returns server-sent events (``data: {"choices":
[{"delta": {"content"}}]}`` …, then ``data: [DONE]``), with the usage on a last chunk when
``stream_options.include_usage`` is set.

A stream that stops sending for ``stall_timeout_seconds`` raises ``TIMEOUT``: the client's
read timeout bounds one socket read, but a server that keeps the connection open and sends
nothing must not hold a user's answer forever. Takes an injected ``httpx.AsyncClient``
(base_url, timeout and any bearer key already set); the factory owns its lifetime.
"""

from __future__ import annotations

import asyncio
import json
from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Any, cast, override

import httpx

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import (
    LLMConfig,
    LLMProvider,
    LLMResponse,
    StreamUsage,
    text_only,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.resilience.http_boundary import httpx_error_to_app_error

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.core.interfaces.llm import LLMMessage

CHAT_PATH = "/v1/chat/completions"
"""The OpenAI chat endpoint, relative to the server's base URL."""

_OPERATION = "openai-compatible chat"
"""The call named in coded error messages."""

_DATA = "data:"
"""The server-sent-event field that carries a chunk."""

_DONE = "[DONE]"
"""The payload of the last server-sent event."""


def _finish_reason(reason: object) -> str:
    """Map OpenAI's ``finish_reason`` to the ``LLMResponse`` vocabulary.

    (``length`` or ``stop``).

    Returns:
        The ``LLMResponse`` finish reason.

    """
    return "length" if reason == "length" else "stop"


class _Usage:
    """The usage and finish reason a stream accumulates until its end."""

    def __init__(self, model: str) -> None:
        """Start with the requested model and no counts."""
        self.model = model
        self.input_tokens = 0
        self.output_tokens = 0
        self.finish_reason = "stop"

    def observe(self, chunk: dict[str, Any]) -> None:
        """Record a chunk's model, usage and finish reason, when it carries them."""
        self.model = str(chunk.get("model") or self.model)
        usage = cast("dict[str, Any]", chunk.get("usage") or {})
        if usage:
            self.input_tokens = int(usage.get("prompt_tokens", 0))
            self.output_tokens = int(usage.get("completion_tokens", 0))
        for choice in cast("list[dict[str, Any]]", chunk.get("choices") or []):
            if choice.get("finish_reason"):
                self.finish_reason = _finish_reason(choice["finish_reason"])

    def report(self) -> StreamUsage:
        """Return the accumulated ``StreamUsage``.

        Returns:
            The accumulated ``StreamUsage``.

        """
        return StreamUsage(
            model=self.model,
            input_tokens=self.input_tokens,
            output_tokens=self.output_tokens,
            finish_reason=self.finish_reason,
        )


class OpenAICompatibleLlmProvider(NoOpAsyncResource, LLMProvider):
    """``LLMProvider`` over an OpenAI-compatible ``/v1/chat/completions`` server."""

    def __init__(
        self, client: httpx.AsyncClient, model: str, *, stall_timeout_seconds: float
    ) -> None:
        """Bind the client.

        The default model and the longest silence a stream may keep.
        """
        self._client = client
        self._model = model
        self._stall_timeout = stall_timeout_seconds

    def _payload(
        self, messages: list[LLMMessage], config: LLMConfig | None, *, stream: bool
    ) -> dict[str, object]:
        """Build the chat request body from the messages and the ``LLMConfig``.

        Returns:
            The chat request body from the messages and the ``LLMConfig``.

        """
        cfg = config or LLMConfig()
        payload: dict[str, object] = {
            "model": cfg.model or self._model,
            "messages": [{"role": m.role, "content": m.content} for m in messages],
            "temperature": cfg.temperature,
            "max_tokens": cfg.max_tokens,
            "top_p": cfg.top_p,
            "stream": stream,
        }
        if cfg.stop_sequences:
            payload["stop"] = cfg.stop_sequences
        if stream:
            payload["stream_options"] = {"include_usage": True}
        return payload

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Generate one completion (a non-streaming chat call).

        A callee raises ``AppError``: ``TIMEOUT``, ``UNAVAILABLE`` (unreachable, 429, 5xx)
        or ``INTERNAL`` (another status, an undecodable body).

        Returns:
            The completion.

        Raises:
            httpx_error_to_app_error: The coded error for a transport failure.

        """
        payload = self._payload(messages, config, stream=False)
        try:
            response = await self._client.post(CHAT_PATH, json=payload)
            response.raise_for_status()
            body = cast("dict[str, Any]", response.json())
        except (httpx.HTTPError, ValueError) as exc:
            raise httpx_error_to_app_error(exc, _OPERATION) from exc
        choices = cast("list[dict[str, Any]]", body.get("choices") or [{}])
        message = cast("dict[str, Any]", choices[0].get("message") or {})
        usage = cast("dict[str, Any]", body.get("usage") or {})
        return LLMResponse(
            content=str(message.get("content") or ""),
            model=str(body.get("model") or payload["model"]),
            input_tokens=int(usage.get("prompt_tokens", 0)),
            output_tokens=int(usage.get("completion_tokens", 0)),
            finish_reason=_finish_reason(choices[0].get("finish_reason")),
        )

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Stream a completion's text deltas.

        Awaited to obtain the iterator, then ``async for``.

        Returns:
            The stream of text deltas.

        """
        return text_only(await self.stream_with_usage(messages, config))

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Stream the text deltas in order, then the usage as a ``StreamUsage``.

        Returns:
            The stream of text deltas, ending with the usage when the provider reports it.

        """
        return self._stream(self._payload(messages, config, stream=True))

    async def _stream(
        self, payload: dict[str, object]
    ) -> AsyncIterator[str | StreamUsage]:
        """Yield each chunk's delta until ``[DONE]`` (or the stream's end).

        Then the usage.

        The response stays open across the yields; the ``finally`` releases it when the
        stream ends, fails, or the consumer closes this generator.

        Yields:
            Each chunk's delta until ``[DONE]`` (or the stream's end).

        A callee raises ``AppError``: As ``complete``; ``TIMEOUT`` when no line arrives
        within the stall timeout.

        Raises:
            httpx_error_to_app_error: The coded error for a transport failure.

        """
        stack = AsyncExitStack()
        try:
            response = await stack.enter_async_context(
                self._client.stream("POST", CHAT_PATH, json=payload)
            )
            response.raise_for_status()
        except httpx.HTTPError as exc:
            await stack.aclose()
            raise httpx_error_to_app_error(exc, _OPERATION) from exc
        usage = _Usage(str(payload["model"]))
        try:
            async for chunk in self._chunks(response):
                usage.observe(chunk)
                for choice in cast("list[dict[str, Any]]", chunk.get("choices") or []):
                    delta = cast("dict[str, Any]", choice.get("delta") or {})
                    if text := delta.get("content"):
                        yield str(text)
            yield usage.report()
        finally:
            await stack.aclose()

    async def _chunks(self, response: httpx.Response) -> AsyncIterator[dict[str, Any]]:
        """Decode each ``data:`` event until ``[DONE]``.

        Waiting at most the stall timeout per line.

        Yields:
            Each decoded event.

        Raises:
            httpx_error_to_app_error: The coded error for a transport failure.
            AppError: ``_OPERATION`` stream stalled for ``self._stall_timeout``s.

        """
        lines = response.aiter_lines()
        while True:
            try:
                async with asyncio.timeout(self._stall_timeout):
                    line = await anext(lines)
            except StopAsyncIteration:
                return
            except TimeoutError as exc:
                msg = f"{_OPERATION} stream stalled for {self._stall_timeout:g}s"
                raise AppError(ErrorCode.TIMEOUT, msg, cause=exc) from exc
            except httpx.HTTPError as exc:
                raise httpx_error_to_app_error(exc, _OPERATION) from exc
            if not line.startswith(_DATA):
                continue
            data = line[len(_DATA) :].strip()
            if data == _DONE:
                return
            try:
                chunk = cast("dict[str, Any]", json.loads(data))
            except ValueError as exc:
                raise httpx_error_to_app_error(exc, _OPERATION) from exc
            yield chunk

    @override
    def model_name(self) -> str:
        """Return the default served model.

        Returns:
            The default served model.

        """
        return self._model
