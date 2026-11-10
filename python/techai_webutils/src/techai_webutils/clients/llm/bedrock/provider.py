"""Bedrock LLM provider (provider-agnostic via the Bedrock Converse API).

Thin aiobotocore wrapper used in stage/prod (dev uses the stub). The Converse /
ConverseStream API is a single, model-agnostic request/response shape that works across
every Bedrock provider (Amazon Nova, Anthropic Claude, Meta Llama, ...), so this client is
not tied to any one model's native ``InvokeModel`` body format — switching models is a
config change, not a code change. Carved out of unit coverage; exercised against real
Bedrock in staging.
"""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Protocol, Self, TypedDict, cast, final, override

import aiobotocore.session
from botocore.exceptions import BotoCoreError, ClientError

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


class _Block(TypedDict, total=False):
    """One content block or text delta."""

    text: str
    """The text."""


class _Message(TypedDict, total=False):
    """A Converse answer message."""

    content: list[_Block]
    """Its content blocks."""


class _Output(TypedDict, total=False):
    """The ``output`` of a Converse response."""

    message: _Message
    """The answer."""


class _Usage(TypedDict, total=False):
    """Converse token counts."""

    inputTokens: int
    """Input tokens."""

    outputTokens: int
    """Output tokens."""


class _DeltaEvent(TypedDict, total=False):
    """A ``contentBlockDelta`` event."""

    delta: _Block
    """The text delta."""


class _StopEvent(TypedDict, total=False):
    """A ``messageStop`` event."""

    stopReason: str
    """Why generation stopped."""


class _MetadataEvent(TypedDict, total=False):
    """A ``metadata`` event."""

    usage: _Usage
    """The token counts."""


class _StreamEvent(TypedDict, total=False):
    """One ConverseStream event."""

    contentBlockDelta: _DeltaEvent
    """A text delta."""

    messageStop: _StopEvent
    """The end of the message."""

    metadata: _MetadataEvent
    """The usage."""


class _ConverseResponse(TypedDict, total=False):
    """The Converse response fields this provider reads."""

    output: _Output
    """The answer."""

    usage: _Usage
    """The token counts."""

    stopReason: str
    """Why generation stopped."""

    stream: AsyncIterator[_StreamEvent]
    """The ConverseStream events."""


class _BedrockRuntimeClient(Protocol):
    """The bedrock-runtime Converse slice this provider calls.

    ``create_client("bedrock-runtime")`` has no typed overload (it resolves to ``Never``),
    so the yielded client is cast to this Protocol. Both calls take the model-agnostic
    Converse request (``messages``/``system``/``inferenceConfig``) and are described with
    ``**kwargs`` because aiobotocore is unstubbed.
    """

    async def converse(self, **kwargs: object) -> _ConverseResponse:
        """Invoke once; returns the full Converse response dict.

        (output/usage/stopReason).

        Args:
            **kwargs: The Converse request fields.

        Returns:
            The Converse response.

        """
        ...

    async def converse_stream(self, **kwargs: object) -> _ConverseResponse:
        """Invoke with streaming; the returned ``stream`` is an async event iterator.

        Args:
            **kwargs: The ConverseStream request fields.

        Returns:
            The response, whose ``stream`` is an async event iterator.

        """
        ...


_DEFAULT_MAX_TOKENS = 1024
"""Default cap on generated tokens when the caller supplies no per-call config."""
_DEFAULT_TEMPERATURE = 0.7
"""Default sampling temperature when the caller supplies no per-call config."""


def _converse_args(
    model: str, messages: list[LLMMessage], config: LLMConfig | None
) -> dict[str, object]:
    """Build the model-agnostic Converse request from messages + config.

    System messages become the top-level ``system`` blocks (omitted when there are none —
    Converse rejects an empty ``system`` list); the remaining turns map to ``{role,
    content:[{text}]}``. This shape is identical for Nova, Claude, Llama, etc., which is
    the whole point of Converse. The decoding dials (``maxTokens`` and ``temperature``)
    come from the per-call ``config`` when the caller supplies one (the consumer builds it
    from its own config); otherwise the conservative provider defaults apply. ``topP`` is
    deliberately not sent: Claude on Converse rejects ``temperature`` and ``topP``
    together, and the retrieval path tunes temperature.

    Args:
        model: The model id.
        messages: The conversation.
        config: Per-call settings; ``None`` uses the defaults.

    Returns:
        The model-agnostic Converse request from messages + config.

    """
    system = [{"text": m.content} for m in messages if m.role == "system"]
    turns = [
        {"role": m.role, "content": [{"text": m.content}]}
        for m in messages
        if m.role != "system"
    ]
    # Bedrock Converse rejects `temperature` and `topP` together for Claude models
    # ("`temperature` and `top_p` cannot both be specified for this model"), so the
    # request carries only temperature — the decoding dial the retrieval path tunes for
    # faithfulness.
    args: dict[str, object] = {
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
    """Map a Bedrock/botocore failure.

    To a coded ``AppError`` (ARCHITECTURE.md#error-codes).

    Delegates to the shared ``foundation/resilience/aws_boundary`` mapping (throttling /
    5xx / model-timeout → transient ``UNAVAILABLE``; access-denied, ``ResourceNotFound``,
    validation → terminal ``INTERNAL``). An unwrapped ``ClientError`` would also escape
    structlog as a multi-KB frame-locals dump.

    Args:
        exc: The SDK's error.

    Returns:
        The coded error.

    """
    return botocore_error_to_app_error(exc, "bedrock converse")


async def _coded_events(
    stream: AsyncIterator[_StreamEvent],
) -> AsyncIterator[_StreamEvent]:
    """Re-yield the ConverseStream events, wrapping a mid-stream botocore failure.

    A ``ClientError`` / ``BotoCoreError`` raised while iterating becomes a coded
    ``AppError`` (``_to_app_error``), so the stream never leaks a raw botocore exception.

    Args:
        stream: The ConverseStream events.

    Yields:
        Each ConverseStream event.

    Raises:
        _to_app_error: The coded error for a provider failure.

    """
    try:
        async for event in stream:
            yield event
    except (ClientError, BotoCoreError) as exc:
        raise _to_app_error(exc) from exc


@final
class BedrockLlmProvider(LLMProvider):
    """LLMProvider backed by the AWS Bedrock Converse API (model-agnostic).

    A single bedrock-runtime client is opened lazily on first use and reused across
    ``complete`` + ``stream`` calls (rather than one per invocation on the hot generation
    path); call ``aclose`` on shutdown to release it. Any Bedrock model id
    (``amazon.nova-*``, ``us.anthropic.*``, ...) works without a code change.
    """

    def __init__(self, *, region: str, model: str, endpoint: str | None = None) -> None:
        """Store the region, model id, and optional endpoint.

        The client is opened lazily on first use.

        Args:
            region: The AWS region.
            model: The default model id.
            endpoint: An endpoint override; ``None`` uses the region's.

        """
        self._region = region
        self._model = model
        self._endpoint = endpoint
        self._session = aiobotocore.session.get_session()
        # The exit stack that owns the opened client's async context (entered on first
        # use).
        self._stack: AsyncExitStack | None = None
        self._client: _BedrockRuntimeClient | None = None

    async def _runtime_client(self) -> _BedrockRuntimeClient:
        """Return the shared bedrock-runtime client, opening.

        (and caching) it on first use.

        Returns:
            The shared bedrock-runtime client, opening.

        """
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

    @override
    async def __aenter__(self) -> Self:
        """Enter the async context; the shared client opens lazily on first call.

        Returns:
            This instance.

        """
        return self

    @override
    async def __aexit__(self, *exc: object) -> None:
        """Release the shared client on context exit.

        Args:
            *exc: The exception triple, if any.

        """
        await self.aclose()

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Invoke the model once via Converse and return the full completion.

        Bedrock/botocore failures are wrapped in a coded ``AppError`` (``_to_app_error``)
        so a raw ``ClientError`` never crosses the provider boundary.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings (model, temperature, token limit); ``None`` uses the
                provider's defaults.

        Returns:
            The completion.

        Raises:
            _to_app_error: The coded error for a provider failure.

        """
        client = await self._runtime_client()
        try:
            response = await client.converse(
                **_converse_args(self._model, messages, config)
            )
        except (ClientError, BotoCoreError) as exc:
            raise _to_app_error(exc) from exc
        message = response.get("output", _Output()).get("message", _Message())
        usage = response.get("usage", _Usage())
        return LLMResponse(
            content="".join(
                block.get("text", "") for block in message.get("content", [])
            ),
            model=self._model,
            input_tokens=int(usage.get("inputTokens", 0)),
            output_tokens=int(usage.get("outputTokens", 0)),
            finish_reason=str(response.get("stopReason", "stop")),
        )

    @override
    async def stream(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None = None,
    ) -> AsyncIterator[str]:
        """Return an async iterator of text deltas.

        From a streaming Converse invocation.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings; ``None`` uses the provider's defaults.

        Returns:
            An async iterator of text deltas.

        """
        return text_only(await self.stream_with_usage(messages, config))

    @override
    async def stream_with_usage(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None = None,
    ) -> AsyncIterator[str | StreamUsage]:
        """Return text deltas from a streaming Converse invocation.

        Then its ``metadata`` usage.

        Args:
            messages: The conversation so far, oldest first.
            config: Per-call settings; ``None`` uses the provider's defaults.

        Returns:
            Text deltas from a streaming Converse invocation.

        """
        return self._stream_deltas(messages, config)

    async def _open_stream(
        self, messages: list[LLMMessage], config: LLMConfig | None
    ) -> AsyncIterator[_StreamEvent]:
        """Open a ConverseStream invocation.

        Args:
            messages: The conversation.
            config: Per-call settings; ``None`` uses the defaults.

        Returns:
            The invocation's event stream.

        Raises:
            _to_app_error: The coded error for a provider failure.

        """
        client = await self._runtime_client()
        try:
            response = await client.converse_stream(
                **_converse_args(self._model, messages, config)
            )
        except (ClientError, BotoCoreError) as exc:
            raise _to_app_error(exc) from exc
        return cast("AsyncIterator[_StreamEvent]", response.get("stream"))

    async def _stream_deltas(
        self,
        messages: list[LLMMessage],
        config: LLMConfig | None,
    ) -> AsyncIterator[str | StreamUsage]:
        """Open a streaming Converse invocation and yield contentBlockDelta text deltas.

        Then usage.

        ``messageStop`` carries the stop reason and the final ``metadata`` event the token
        usage; together they become the last item, a ``StreamUsage`` (omitted if Bedrock
        sent no ``metadata``). Bedrock/botocore failures (on open or mid-stream) are
        wrapped in a coded ``AppError`` (``_to_app_error``); a ``GeneratorExit`` from
        client cancellation propagates untouched.

        Args:
            messages: The conversation.
            config: Per-call settings; ``None`` uses the defaults.

        Yields:
            A streaming Converse invocation and yield contentBlockDelta text deltas.

        """
        stream = await self._open_stream(messages, config)
        stop_reason = "stop"
        usage: _Usage | None = None
        async for event in _coded_events(stream):
            text = (
                event
                .get("contentBlockDelta", _DeltaEvent())
                .get("delta", _Block())
                .get("text")
            )
            if text:
                yield text
            if "messageStop" in event:
                stop_reason = str(event["messageStop"].get("stopReason", "stop"))
            if "metadata" in event:
                usage = event["metadata"].get("usage", _Usage())
        if usage is not None:
            yield StreamUsage(
                model=self._model,
                input_tokens=int(usage.get("inputTokens", 0)),
                output_tokens=int(usage.get("outputTokens", 0)),
                finish_reason=stop_reason,
            )

    @override
    def model_name(self) -> str:
        """Return the configured Bedrock model id.

        Returns:
            The configured Bedrock model id.

        """
        return self._model
