"""Ollama LLM provider — local dev text generation via the Ollama ``/api/chat`` endpoint.

The local analogue of the Bedrock LLM: instead of AWS, it generates against a local Ollama
server (the same one that serves embeddings), so the RAG generator produces real answers
with no external dependency. Selected by config (``LlmConfig.kind = ollama``). Takes an
injected ``httpx.AsyncClient`` (base_url = the Ollama host) so unit tests inject a mock;
the factory that builds it owns the client's lifetime (process-lived — see
``new_llm_from_config``).

Wire shape: ``POST /api/chat`` with ``{"model", "messages": [{"role","content"}],
"stream", "options"}`` returns ``{"message": {"content"}, "done", "done_reason",
"prompt_eval_count", "eval_count"}`` for a non-streaming call, or newline-delimited JSON
chunks (each ``{"message": {"content"}, "done"}``) when streaming.
"""

from __future__ import annotations

import json
from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, TypedDict, cast, final, override

import httpx

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

    from techai_webutils.core.errors import AppError
    from techai_webutils.core.interfaces.llm import LLMMessage


def _finish_reason(done_reason: str | None) -> str:
    """Map Ollama's ``done_reason`` to the LLMResponse ``finish_reason`` vocabulary.

    Ollama reports ``length`` when it hit ``num_predict`` and ``stop`` otherwise (or
    ``None`` on older servers); anything that isn't an explicit length cap is treated as a
    normal stop.

    Args:
        done_reason: Ollama's ``done_reason``.

    Returns:
        The ``LLMResponse`` finish reason.

    """
    return "length" if done_reason == "length" else "stop"


def _to_app_error(exc: Exception) -> AppError:
    """Map an Ollama transport or decode failure to a coded ``AppError``.

    It uses ``httpx_error_to_app_error``.

    A timeout → ``TIMEOUT``; an unreachable server or a 5xx/429 → ``UNAVAILABLE``
    (transient, retryable); a 4xx or a malformed body → ``INTERNAL`` (terminal).

    Args:
        exc: The transport or decode error.

    Returns:
        The coded error.

    """
    return httpx_error_to_app_error(exc, "ollama chat")


class _WireMessage(TypedDict, total=False):
    """The ``message`` of a reply."""

    content: str
    """The text."""


class _WireChat(TypedDict, total=False):
    """An ``/api/chat`` reply or stream chunk."""

    model: str
    """The model that served it."""

    message: _WireMessage | None
    """The text."""

    done: bool
    """Whether this is the last chunk."""

    done_reason: str | None
    """Why generation stopped."""

    prompt_eval_count: int
    """Input tokens."""

    eval_count: int
    """Output tokens."""


async def _ndjson_chunks(response: httpx.Response) -> AsyncIterator[_WireChat]:
    """Decode each non-blank NDJSON line of a streamed /api/chat response.

    A transport or decode failure mid-stream is wrapped in a coded ``AppError``
    (``_to_app_error``).

    Args:
        response: The streamed response.

    Yields:
        Each decoded chunk.

    Raises:
        _to_app_error: The coded error for a provider failure.

    """
    try:
        async for line in response.aiter_lines():
            if line.strip():
                yield json.loads(line)
    except (httpx.HTTPError, ValueError) as exc:
        raise _to_app_error(exc) from exc


async def _tokens(
    chunks: AsyncIterator[_WireChat], model: str
) -> AsyncIterator[str | StreamUsage]:
    """Yield each chunk's content delta until ``done``, then the ``done`` chunk's usage.

    ``model`` is the requested model, reported when the ``done`` chunk names none.

    Args:
        chunks: The decoded chunks.
        model: The requested model, reported when a chunk names none.

    Yields:
        Each chunk's content delta until ``done``, then the ``done`` chunk's usage.

    """
    async for chunk in chunks:
        message = chunk.get("message") or _WireMessage()
        token = message.get("content", "")
        if token:
            yield token
        if chunk.get("done"):
            yield StreamUsage(
                model=str(chunk.get("model") or model),
                input_tokens=int(chunk.get("prompt_eval_count", 0)),
                output_tokens=int(chunk.get("eval_count", 0)),
                finish_reason=_finish_reason(chunk.get("done_reason")),
            )
            return


@final
class OllamaLlmProvider(NoOpAsyncResource, LLMProvider):
    """``LLMProvider`` backed by a local Ollama server (``/api/chat``).

    ``LLMConfig`` (temperature / max_tokens / top_p / stop) is mapped onto Ollama's
    ``options``; an explicit ``config.model`` overrides the provider's default model for a
    single call.
    """

    def __init__(self, client: httpx.AsyncClient, model: str) -> None:
        """Bind the httpx client (base_url = Ollama host) and the default chat model.

        Args:
            client: An httpx client whose base URL is the Ollama host.
            model: The default chat model.

        """
        self._client = client
        self._model = model

    def _chat_payload(
        self, messages: list[LLMMessage], config: LLMConfig | None, *, stream: bool
    ) -> dict[str, object]:
        """Build the /api/chat request body from the messages and LLMConfig.

        LLMConfig maps onto the options.

        Args:
            messages: The conversation.
            config: Per-call settings; ``None`` uses the defaults.
            stream: Whether to ask for a streamed answer.

        Returns:
            The /api/chat request body from the messages + LLMConfig.

        """
        cfg = config or LLMConfig()
        options: dict[str, object] = {
            "temperature": cfg.temperature,
            "num_predict": cfg.max_tokens,
            "top_p": cfg.top_p,
        }
        if cfg.stop_sequences:
            options["stop"] = cfg.stop_sequences
        return {
            "model": cfg.model or self._model,
            "messages": [{"role": m.role, "content": m.content} for m in messages],
            "stream": stream,
            "options": options,
        }

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Generate a single completion for the message history.

        It makes one non-streaming /api/chat call.

        Transport/decode failures are wrapped in a coded ``AppError`` (``_to_app_error``)
        so a raw ``httpx`` / JSON error never crosses the provider boundary.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings (model, temperature, token limit); ``None`` uses the
                provider's defaults.

        Returns:
            The completion.

        Raises:
            _to_app_error: The coded error for a provider failure.

        """
        try:
            response = await self._client.post(
                "/api/chat", json=self._chat_payload(messages, config, stream=False)
            )
            _ = response.raise_for_status()
            body = cast("_WireChat", response.json())
        except (httpx.HTTPError, ValueError) as exc:
            raise _to_app_error(exc) from exc
        message = body.get("message") or _WireMessage()
        return LLMResponse(
            content=message.get("content", ""),
            model=body.get("model", self._model),
            input_tokens=int(body.get("prompt_eval_count", 0)),
            output_tokens=int(body.get("eval_count", 0)),
            finish_reason=_finish_reason(body.get("done_reason")),
        )

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Stream a completion token by token.

        Await it to obtain the iterator, then ``async for``.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings; ``None`` uses the provider's defaults.

        Returns:
            The stream of text deltas.

        """
        return text_only(await self.stream_with_usage(messages, config))

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Stream content deltas, then the ``done`` chunk's token counts.

        The counts arrive as a ``StreamUsage``.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings; ``None`` uses the provider's defaults.

        Returns:
            The stream of text deltas, ending with the usage when the provider reports it.

        """
        return self._stream_tokens(self._chat_payload(messages, config, stream=True))

    async def _stream_tokens(
        self, payload: dict[str, object]
    ) -> AsyncIterator[str | StreamUsage]:
        """Yield content deltas from the streamed /api/chat NDJSON.

        At ``done`` it yields the usage.

        The ``done`` chunk carries ``prompt_eval_count`` / ``eval_count`` /
        ``done_reason``, which become the final ``StreamUsage``. Transport/decode failures
        are wrapped in a coded ``AppError`` (``_to_app_error``); a ``GeneratorExit`` from
        client cancellation propagates untouched.

        The streamed response stays open across the yields: the ``finally`` releases it
        when the stream ends, fails, or the consumer closes this generator.

        Args:
            payload: The /api/chat request body.

        Yields:
            Content deltas from the streamed /api/chat NDJSON until ``done``.

        Raises:
            _to_app_error: The coded error for a provider failure.

        """
        stack = AsyncExitStack()
        try:
            response = await stack.enter_async_context(
                self._client.stream("POST", "/api/chat", json=payload)
            )
            _ = response.raise_for_status()
        except (httpx.HTTPError, ValueError) as exc:
            await stack.aclose()
            raise _to_app_error(exc) from exc
        try:
            async for item in _tokens(_ndjson_chunks(response), str(payload["model"])):
                yield item
        finally:
            await stack.aclose()

    @override
    def model_name(self) -> str:
        """Return the Ollama chat model identifier.

        Returns:
            The Ollama chat model identifier.

        """
        return self._model
