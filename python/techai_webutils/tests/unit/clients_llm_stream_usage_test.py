"""Unit tests for ``LLMProvider.stream_with_usage`` and its ``StreamUsage`` terminal item.

Streamed generations carry token usage only at the end of the stream (Bedrock's ``metadata`` event,
Ollama's final ``done`` chunk). ``stream_with_usage`` surfaces it as the last item so token metrics
are not empty for streamed traffic, while ``stream()`` stays the text-only API.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any, override
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from techai_webutils.clients.llm.ollama import OllamaLlmProvider
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import (
    LLMConfig,
    LLMMessage,
    LLMProvider,
    LLMResponse,
    StreamUsage,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import AsyncIterator


def _messages(user: str) -> list[LLMMessage]:
    """Return a system + user message pair."""
    return [
        LLMMessage(role="system", content="be brief"),
        LLMMessage(role="user", content=user),
    ]


async def _collect(iterator: AsyncIterator[Any]) -> list[Any]:
    """Drain an async iterator into a list."""
    return [item async for item in iterator]


async def _events(events: list[dict[str, Any]]) -> AsyncIterator[dict[str, Any]]:
    """Yield Converse stream events in order (the shape aiobotocore's EventStream yields)."""
    await asyncio.sleep(0)
    for event in events:
        yield event


async def _lines(lines: list[str]) -> AsyncIterator[str]:
    """Yield NDJSON lines in order (the shape ``httpx.Response.aiter_lines`` yields)."""
    await asyncio.sleep(0)
    for line in lines:
        yield line


def _ollama_client(lines: list[str], *, status: int = 200) -> MagicMock:
    """Return an ``httpx.AsyncClient`` mock whose ``stream()`` context yields the given NDJSON lines.

    The response is a ``MagicMock(spec=httpx.Response)``; a non-2xx ``status`` makes its
    ``raise_for_status`` raise the real ``httpx.HTTPStatusError``.
    """
    response = MagicMock(spec=httpx.Response)
    response.status_code = status
    response.aiter_lines = MagicMock(return_value=_lines(lines))
    if status >= 400:
        request = httpx.Request("POST", "http://ollama/api/chat")
        response.raise_for_status = MagicMock(
            side_effect=httpx.HTTPStatusError(
                "error", request=request, response=httpx.Response(status, request=request)
            )
        )
    stream_ctx = MagicMock()
    stream_ctx.__aenter__ = AsyncMock(return_value=response)
    stream_ctx.__aexit__ = AsyncMock(return_value=False)
    client = MagicMock(spec=httpx.AsyncClient)
    client.stream = MagicMock(return_value=stream_ctx)
    return client


@pytest.mark.asyncio
async def test_bedrock_stream_with_usage_yields_usage_last() -> None:
    """Test that Bedrock's streamed Converse ends with the ``metadata`` usage as a ``StreamUsage``.

    **Why this test is important:**
      - Bedrock reports streamed token usage only in the final ``metadata`` event; dropping it
        leaves every streamed answer with zero tokens in metrics and cost.

    **What it tests:**
      - the text deltas come first, in order
      - the last item is ``StreamUsage(model, 12, 3, "end_turn")`` from ``metadata.usage`` and
        ``messageStop.stopReason``
    """
    from techai_webutils.clients.llm.bedrock import BedrockLlmProvider

    runtime = MagicMock()
    runtime.converse_stream = AsyncMock(
        return_value={
            "stream": _events([
                {"messageStart": {"role": "assistant"}},
                {"contentBlockDelta": {"delta": {"text": "Hello"}}},
                {"contentBlockDelta": {"delta": {"text": " world"}}},
                {"messageStop": {"stopReason": "end_turn"}},
                {
                    "metadata": {
                        "usage": {"inputTokens": 12, "outputTokens": 3, "totalTokens": 15}
                    }
                },
            ])
        }
    )
    client_cm = MagicMock()
    client_cm.__aenter__ = AsyncMock(return_value=runtime)
    client_cm.__aexit__ = AsyncMock(return_value=None)
    session = MagicMock()
    session.create_client = MagicMock(return_value=client_cm)

    with patch("aiobotocore.session.get_session", return_value=session):
        provider = BedrockLlmProvider(region="us-east-1", model="amazon.nova-lite-v1:0")
        items = await _collect(await provider.stream_with_usage(_messages("hi")))

    assert items == [
        "Hello",
        " world",
        StreamUsage(
            model="amazon.nova-lite-v1:0",
            input_tokens=12,
            output_tokens=3,
            finish_reason="end_turn",
        ),
    ]


@pytest.mark.asyncio
async def test_ollama_stream_with_usage_yields_usage_last() -> None:
    """Test that Ollama's streamed chat ends with the ``done`` chunk's counts as a ``StreamUsage``.

    **Why this test is important:**
      - Ollama puts ``prompt_eval_count`` / ``eval_count`` only on the final ``done`` chunk; the
        local stack's token panels depend on it.

    **What it tests:**
      - the content deltas come first, then ``StreamUsage("llama3.2", 7, 2, "stop")``
    """
    client = _ollama_client([
        '{"model":"llama3.2","message":{"content":"Ish"},"done":false}',
        '{"model":"llama3.2","message":{"content":"mael"},"done":false}',
        (
            '{"model":"llama3.2","message":{"content":""},"done":true,"done_reason":"stop",'
            '"prompt_eval_count":7,"eval_count":2}'
        ),
    ])

    items = await _collect(
        await OllamaLlmProvider(client, model="llama3.2").stream_with_usage(
            _messages("hi")
        )
    )

    assert items == [
        "Ish",
        "mael",
        StreamUsage(
            model="llama3.2", input_tokens=7, output_tokens=2, finish_reason="stop"
        ),
    ]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("status", "code"), [(503, ErrorCode.UNAVAILABLE), (400, ErrorCode.INTERNAL)]
)
async def test_ollama_stream_with_usage_codes_http_errors(
    status: int, code: ErrorCode
) -> None:
    """Test that a non-2xx streamed Ollama response raises a coded ``AppError``.

    **Why this test is important:**
      - Retry and the circuit breaker classify by ``ErrorCode``; a raw ``httpx.HTTPStatusError``
        would bypass them and a 5xx would never be retried.

    **What it tests:**
      - HTTP 503 raises ``AppError(UNAVAILABLE)`` and HTTP 400 raises ``AppError(INTERNAL)``,
        each with the message "ollama chat returned HTTP <status>", and no item is yielded
    """
    client = _ollama_client(
        ['{"message":{"content":"never"},"done":false}'], status=status
    )
    stream = await OllamaLlmProvider(client, model="m").stream_with_usage(_messages("hi"))

    with pytest.raises(AppError) as excinfo:
        await _collect(stream)

    assert excinfo.value.code == code
    assert excinfo.value.message == f"ollama chat returned HTTP {status}"


async def _lines_then_fail(lines: list[str], exc: Exception) -> AsyncIterator[str]:
    """Yield NDJSON lines in order, then raise ``exc`` as a dropped stream would."""
    await asyncio.sleep(0)
    for line in lines:
        yield line
    raise exc


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("tail", "code", "message"),
    [
        ("not json", ErrorCode.INTERNAL, "ollama chat decode error"),
        (
            httpx.ReadError("connection reset"),
            ErrorCode.UNAVAILABLE,
            "ollama chat unreachable",
        ),
    ],
)
async def test_ollama_stream_codes_mid_stream_failures(
    tail: str | Exception, code: ErrorCode, message: str
) -> None:
    """Test that a failure after the stream has started still surfaces as a coded ``AppError``.

    **Why this test is important:**
      - The status check covers only the response head; a body that turns malformed or a
        connection that drops mid-stream would otherwise leak a raw ``ValueError`` or
        ``httpx`` error past the provider, bypassing retry and transport status mapping.

    **What it tests:**
      - the tokens before the failure are yielded first
      - a malformed line raises ``AppError(INTERNAL)`` and a dropped connection raises
        ``AppError(UNAVAILABLE)``, each with the matching message prefix
    """
    first = '{"message":{"content":"partial"},"done":false}'
    client = _ollama_client([])
    response = client.stream.return_value.__aenter__.return_value
    if isinstance(tail, Exception):
        response.aiter_lines = MagicMock(return_value=_lines_then_fail([first], tail))
    else:
        response.aiter_lines = MagicMock(return_value=_lines([first, tail]))
    stream = await OllamaLlmProvider(client, model="m").stream_with_usage(_messages("hi"))

    assert await anext(stream) == "partial"
    with pytest.raises(AppError) as excinfo:
        await anext(stream)

    assert excinfo.value.code == code
    assert excinfo.value.message.startswith(message)


async def _events_then_fail(
    events: list[dict[str, Any]], exc: Exception
) -> AsyncIterator[dict[str, Any]]:
    """Yield Converse stream events in order, then raise ``exc`` as a failing EventStream would."""
    await asyncio.sleep(0)
    for event in events:
        yield event
    raise exc


@pytest.mark.asyncio
async def test_bedrock_stream_codes_mid_stream_client_errors() -> None:
    """Test that a botocore error raised mid-stream surfaces as a coded ``AppError``.

    **Why this test is important:**
      - Bedrock can throttle or fail after ConverseStream has returned; a raw ``ClientError``
        escaping the stream bypasses retry and the circuit breaker, and dumps its frame locals
        into the log.

    **What it tests:**
      - the text delta before the failure is yielded first
      - a mid-stream ``ThrottlingException`` raises ``AppError(UNAVAILABLE)``
    """
    from botocore.exceptions import ClientError

    from techai_webutils.clients.llm.bedrock import BedrockLlmProvider

    throttled = ClientError(
        {"Error": {"Code": "ThrottlingException", "Message": "slow down"}},
        "ConverseStream",
    )
    runtime = MagicMock()
    runtime.converse_stream = AsyncMock(
        return_value={
            "stream": _events_then_fail(
                [{"contentBlockDelta": {"delta": {"text": "Hel"}}}], throttled
            )
        }
    )
    client_cm = MagicMock()
    client_cm.__aenter__ = AsyncMock(return_value=runtime)
    client_cm.__aexit__ = AsyncMock(return_value=None)
    session = MagicMock()
    session.create_client = MagicMock(return_value=client_cm)

    with patch("aiobotocore.session.get_session", return_value=session):
        provider = BedrockLlmProvider(region="us-east-1", model="amazon.nova-lite-v1:0")
        stream = await provider.stream_with_usage(_messages("hi"))
        assert await anext(stream) == "Hel"
        with pytest.raises(AppError) as excinfo:
            await anext(stream)

    assert excinfo.value.code == ErrorCode.UNAVAILABLE


@pytest.mark.asyncio
async def test_stub_stream_yields_to_the_event_loop_between_tokens() -> None:
    """Test that the stub stream lets other tasks run between tokens, like a network stream.

    **Why this test is important:**
      - A consumer that cancels or times out mid-stream is only exercised against the stub if
        the stub suspends between tokens; a stream that never yields to the loop runs to the
        end before any other task (a cancel, a deadline) can act.

    **What it tests:**
      - a concurrent task records a tick before the stub's last token is consumed
    """
    order: list[str] = []
    stream = await StubLlmProvider().stream(_messages("alpha beta gamma"))

    async def consume() -> None:
        while (token := await anext(stream, None)) is not None:
            order.append(token)

    async def tick() -> None:
        await asyncio.sleep(0)
        order.append("tick")

    await asyncio.gather(consume(), tick())

    assert order.index("tick") < order.index("gamma ")


@pytest.mark.asyncio
async def test_stub_stream_with_usage_yields_usage_last() -> None:
    """Test that the stub streams its echo and then a fixed, deterministic ``StreamUsage``.

    **Why this test is important:**
      - Dev and CI run the stub; the streaming usage path must be exercisable with zero infra.

    **What it tests:**
      - tokens of the last user message, then ``StreamUsage("stub-llm", 4, 2, "stop")`` (word counts)
    """
    items = await _collect(
        await StubLlmProvider().stream_with_usage(_messages("alpha beta"))
    )

    assert items == [
        "alpha ",
        "beta ",
        StreamUsage(
            model="stub-llm", input_tokens=4, output_tokens=2, finish_reason="stop"
        ),
    ]


@pytest.mark.asyncio
async def test_stream_filters_out_usage() -> None:
    """Test that ``stream()`` on a usage-aware provider yields only text.

    **Why this test is important:**
      - Every existing caller of ``stream()`` concatenates strings; a ``StreamUsage`` leaking into
        it would corrupt the streamed answer.

    **What it tests:**
      - the stub's and Ollama's ``stream()`` yield exactly the text deltas, no ``StreamUsage``
    """
    client = _ollama_client([
        '{"message":{"content":"ok"},"done":false}',
        '{"message":{"content":""},"done":true,"prompt_eval_count":1,"eval_count":1}',
    ])

    stub_items = await _collect(await StubLlmProvider().stream(_messages("alpha beta")))
    ollama_items = await _collect(
        await OllamaLlmProvider(client, model="m").stream(_messages("hi"))
    )

    assert stub_items == ["alpha ", "beta "]
    assert ollama_items == ["ok"]


class _TextOnlyProvider(NoOpAsyncResource, LLMProvider):
    """A provider implementing only the abstract API, as a third-party provider would."""

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        raise NotImplementedError

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        async def _gen() -> AsyncIterator[str]:
            await asyncio.sleep(0)
            yield "a"
            yield "b"

        return _gen()

    def model_name(self) -> str:
        return "text-only"


@pytest.mark.asyncio
async def test_default_stream_with_usage_yields_no_usage() -> None:
    """Test that the base ``stream_with_usage`` re-yields ``stream()`` and adds no usage.

    **Why this test is important:**
      - Providers written before ``stream_with_usage`` existed must keep working unchanged; the
        default must not invent usage numbers.

    **What it tests:**
      - a provider overriding only ``stream()`` yields exactly ``["a", "b"]`` from ``stream_with_usage``
    """
    items = await _collect(await _TextOnlyProvider().stream_with_usage(_messages("x")))

    assert items == ["a", "b"]
