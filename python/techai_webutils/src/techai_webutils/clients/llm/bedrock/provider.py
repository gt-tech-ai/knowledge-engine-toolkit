"""Bedrock LLM provider (provider-agnostic via the Bedrock Converse API).

Thin aiobotocore wrapper used in stage/prod (dev uses the stub). The Converse / ConverseStream
API is a single, model-agnostic request/response shape that works across every Bedrock provider
(Amazon Nova, Anthropic Claude, Meta Llama, ...), so this client is not tied to any one model's
native ``InvokeModel`` body format — switching models is a config change, not a code change.
Carved out of unit coverage; exercised against real Bedrock in staging.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Any, Protocol, Self, cast

import aiobotocore.session  # type: ignore[import-untyped]
from botocore.exceptions import BotoCoreError, ClientError  # type: ignore[import-untyped]

from techai_webutils.core.interfaces.llm import (
    LLMProvider,
    LLMResponse,
    StreamUsage,
    text_only,
)
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.core.errors import AppError
    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage


class _BedrockRuntimeClient(Protocol):
    """The bedrock-runtime Converse slice this provider calls.

    ``create_client("bedrock-runtime")`` has no typed overload (it resolves to ``Never``), so the
    yielded client is cast to this Protocol. Both calls take the model-agnostic Converse request
    (``messages``/``system``/``inferenceConfig``) and are described with ``**kwargs`` because
    aiobotocore is unstubbed.
    """

    async def converse(self, **kwargs: object) -> dict[str, Any]:
        """Invoke once; returns the full Converse response dict (output/usage/stopReason)."""
        ...

    async def converse_stream(self, **kwargs: object) -> dict[str, Any]:
        """Invoke with streaming; the returned ``stream`` is an async event iterator."""
        ...


_DEFAULT_MAX_TOKENS = 1024
"""Default cap on generated tokens when the caller supplies no per-call config."""
_DEFAULT_TEMPERATURE = 0.7
"""Default sampling temperature when the caller supplies no per-call config."""


def _converse_args(
    model: str, messages: list[LLMMessage], config: LLMConfig | None
) -> dict[str, Any]:
    """Build the model-agnostic Converse request from messages + config.

    System messages become the top-level ``system`` blocks (omitted when there are none — Converse
    rejects an empty ``system`` list); the remaining turns map to ``{role, content:[{text}]}``. This
    shape is identical for Nova, Claude, Llama, etc., which is the whole point of Converse. The
    decoding dials (``maxTokens`` and ``temperature``) come from the per-call ``config`` when the
    caller supplies one (the consumer builds it from its own config); otherwise the conservative
    provider defaults apply. ``topP`` is deliberately not sent: Claude on
    Converse rejects ``temperature`` and ``topP`` together, and the retrieval path tunes temperature.
    """
    system = [{"text": m.content} for m in messages if m.role == "system"]
    turns = [
        {"role": m.role, "content": [{"text": m.content}]}
        for m in messages
        if m.role != "system"
    ]
    # Bedrock Converse rejects `temperature` and `topP` together for Claude models
    # ("`temperature` and `top_p` cannot both be specified for this model"), so the request carries
    # only temperature — the decoding dial the retrieval path tunes for faithfulness.
    args: dict[str, Any] = {
        "modelId": model,
        "messages": turns,
        "inferenceConfig": {
            "maxTokens": config.max_tokens if config else _DEFAULT_MAX_TOKENS,
            "temperature": config.temperature if config else _DEFAULT_TEMPERATURE,
        },
    }
    if system:
        args["system"] = system
    return args


def _to_app_error(exc: ClientError | BotoCoreError) -> AppError:
    """Map a Bedrock/botocore failure to a coded ``AppError`` (ARCHITECTURE.md#error-codes).

    Delegates to the shared ``foundation/resilience/aws_boundary`` mapping (throttling / 5xx /
    model-timeout → transient ``UNAVAILABLE``; access-denied, ``ResourceNotFound``, validation →
    terminal ``INTERNAL``). An unwrapped ``ClientError`` would also escape structlog as a multi-KB
    frame-locals dump.
    """
    return botocore_error_to_app_error(exc, "bedrock converse")


async def _coded_events(
    stream: AsyncIterator[dict[str, Any]],
) -> AsyncIterator[dict[str, Any]]:
    """Re-yield the ConverseStream events, wrapping a mid-stream botocore failure.

    A ``ClientError`` / ``BotoCoreError`` raised while iterating becomes a coded ``AppError``
    (``_to_app_error``), so the stream never leaks a raw botocore exception.
    """
    try:
        async for event in stream:
            yield event
    except (ClientError, BotoCoreError) as exc:
        raise _to_app_error(exc) from exc


class BedrockLlmProvider(LLMProvider):
    """LLMProvider backed by the AWS Bedrock Converse API (model-agnostic).

    A single bedrock-runtime client is opened lazily on first use and reused across ``complete`` +
    ``stream`` calls (rather than one per invocation on the hot generation path); call ``aclose`` on
    shutdown to release it. Any Bedrock model id (``amazon.nova-*``, ``us.anthropic.*``, ...) works
    without a code change.
    """

    def __init__(self, *, region: str, model: str, endpoint: str | None = None) -> None:
        """Store the region, model id, and optional endpoint; the client is opened lazily on first use."""
        self._region = region
        self._model = model
        self._endpoint = endpoint
        self._session = aiobotocore.session.get_session()
        # The exit stack that owns the opened client's async context (entered on first use).
        self._stack: AsyncExitStack | None = None
        self._client: _BedrockRuntimeClient | None = None

    async def _runtime_client(self) -> _BedrockRuntimeClient:
        """Return the shared bedrock-runtime client, opening (and caching) it on first use."""
        if self._client is None:
            self._stack = stack = AsyncExitStack()
            client_cm = self._session.create_client(
                "bedrock-runtime",
                region_name=self._region,
                endpoint_url=self._endpoint,
            )
            self._client = cast(
                "_BedrockRuntimeClient", await stack.enter_async_context(client_cm)
            )
        return self._client

    async def aclose(self) -> None:
        """Close the shared client if one was opened (idempotent)."""
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._client = None

    async def __aenter__(self) -> Self:
        """Enter the async context; the shared client opens lazily on first call."""
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Release the shared client on context exit."""
        await self.aclose()

    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Invoke the model once via Converse and return the full completion.

        Bedrock/botocore failures are wrapped in a coded ``AppError`` (``_to_app_error``) so a raw
        ``ClientError`` never crosses the provider boundary.
        """
        client = await self._runtime_client()
        try:
            response = await client.converse(
                **_converse_args(self._model, messages, config)
            )
        except (ClientError, BotoCoreError) as exc:
            raise _to_app_error(exc) from exc
        message = cast("dict[str, Any]", response.get("output", {})).get("message", {})
        usage = cast("dict[str, Any]", response.get("usage", {}))
        return LLMResponse(
            content="".join(
                block.get("text", "") for block in message.get("content", [])
            ),
            model=self._model,
            input_tokens=int(usage.get("inputTokens", 0)),
            output_tokens=int(usage.get("outputTokens", 0)),
            finish_reason=str(response.get("stopReason", "stop")),
        )

    async def stream(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None = None,
    ) -> AsyncIterator[str]:
        """Return an async iterator of text deltas from a streaming Converse invocation."""
        return text_only(await self.stream_with_usage(messages, config))

    async def stream_with_usage(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None = None,
    ) -> AsyncIterator[str | StreamUsage]:
        """Return text deltas from a streaming Converse invocation, then its ``metadata`` usage."""
        return self._stream_deltas(messages, config)

    async def _stream_deltas(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None,
    ) -> AsyncIterator[str | StreamUsage]:
        """Open a streaming Converse invocation and yield contentBlockDelta text deltas, then usage.

        ``messageStop`` carries the stop reason and the final ``metadata`` event the token usage;
        together they become the last item, a ``StreamUsage`` (omitted if Bedrock sent no
        ``metadata``). Bedrock/botocore failures (on open or mid-stream) are wrapped in a coded
        ``AppError`` (``_to_app_error``); a ``GeneratorExit`` from client cancellation propagates
        untouched.
        """
        client = await self._runtime_client()
        try:
            response = await client.converse_stream(
                **_converse_args(self._model, messages, config)
            )
        except (ClientError, BotoCoreError) as exc:
            raise _to_app_error(exc) from exc
        stream = cast("AsyncIterator[dict[str, Any]]", response["stream"])
        stop_reason = "stop"
        usage: dict[str, Any] | None = None
        async for event in _coded_events(stream):
            text = event.get("contentBlockDelta", {}).get("delta", {}).get("text")
            if text:
                yield text
            if "messageStop" in event:
                stop_reason = str(event["messageStop"].get("stopReason", "stop"))
            if "metadata" in event:
                usage = cast("dict[str, Any]", event["metadata"].get("usage", {}))
        if usage is not None:
            yield StreamUsage(
                model=self._model,
                input_tokens=int(usage.get("inputTokens", 0)),
                output_tokens=int(usage.get("outputTokens", 0)),
                finish_reason=stop_reason,
            )

    def model_name(self) -> str:
        """Return the configured Bedrock model id."""
        return self._model
