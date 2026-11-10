"""Unit tests for the OpenAI-compatible LLM provider (mocked httpx transport)."""

from __future__ import annotations

import asyncio
import json
from typing import TYPE_CHECKING, cast
from unittest.mock import patch

import httpx
import pytest

from techai_webutils.clients.llm.builder import (
    LlmConfig,
    LlmKind,
    new_llm_from_config,
)
from techai_webutils.clients.llm.openai_compatible import OpenAICompatibleLlmProvider
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, StreamUsage

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Callable

_MESSAGES = [
    LLMMessage(role="system", content="be brief"),
    LLMMessage(role="user", content="hi"),
]


def _provider(
    handler: Callable[[httpx.Request], httpx.Response], *, stall: float = 5.0
) -> OpenAICompatibleLlmProvider:
    """Return a provider whose server is ``handler``.

    Args:
        handler: Answers each request.
        stall: Seconds the stream may stall.

    Returns:
        A provider whose server is ``handler``.

    """
    client = httpx.AsyncClient(
        base_url="http://llm:8000", transport=httpx.MockTransport(handler)
    )
    return OpenAICompatibleLlmProvider(client, "qwen", stall_timeout_seconds=stall)


def _sse(*chunks: dict[str, object]) -> bytes:
    """Encode ``chunks`` as server-sent events ending in ``[DONE]``.

    Args:
        *chunks: The server-sent events.

    Returns:
        The encoded events.

    """
    events = [f"data: {json.dumps(chunk)}\n\n" for chunk in chunks]
    return ("".join(events) + "data: [DONE]\n\n").encode()


def _delta(text: str, finish: str | None = None) -> dict[str, object]:
    """Return one streamed chunk carrying ``text``.

    Args:
        text: The delta's text.
        finish: The finish reason; ``None`` while streaming.

    Returns:
        One streamed chunk carrying ``text``.

    """
    return {"choices": [{"delta": {"content": text}, "finish_reason": finish}]}


async def _drain_into(
    received: list[str | StreamUsage], items: AsyncIterator[str | StreamUsage]
) -> None:
    """Append each streamed item to ``received`` as it arrives.

    They are kept when the stream then fails.

    Args:
        received: Receives each item.
        items: The stream.

    """
    iterator = aiter(items)
    while True:
        try:
            item = await anext(iterator)
        except StopAsyncIteration:
            return
        received.append(item)


async def _collect(items: AsyncIterator[str | StreamUsage]) -> list[str | StreamUsage]:
    """Drain a stream into a list.

    Args:
        items: The stream.

    Returns:
        The drained items.

    """
    return [item async for item in items]


@pytest.mark.asyncio
async def test_complete_maps_the_chat_shape() -> None:
    """Test that ``complete`` posts the chat body and maps the answer.

    The answer, usage and finish reason.

    **Why this test is important:**
      - A self-hosted model behind vLLM or LiteLLM must answer exactly as Bedrock does to
        the generator and the token ledger.

    **What it tests:**
      - one POST to ``/v1/chat/completions`` with the model, the role-tagged messages, the
        sampling settings and the stop sequences, ``stream`` false
      - the content, model, token counts and a ``length`` finish reason are mapped
    """
    sent: list[dict[str, object]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        """Check the path, record the body, and answer a chat completion.

        Args:
            request: The HTTP request.

        Returns:
            The completion response.

        """
        assert request.url.path == "/v1/chat/completions"
        sent.append(cast("dict[str, object]", json.loads(request.content)))
        return httpx.Response(
            200,
            json={
                "model": "qwen-7b",
                "choices": [{"message": {"content": "hello"}, "finish_reason": "length"}],
                "usage": {"prompt_tokens": 9, "completion_tokens": 2},
            },
        )

    response = await _provider(handler).complete(
        _MESSAGES, LLMConfig(temperature=0.1, max_tokens=50, stop_sequences=["\n\n"])
    )

    assert sent == [
        {
            "model": "qwen",
            "messages": [
                {"role": "system", "content": "be brief"},
                {"role": "user", "content": "hi"},
            ],
            "temperature": 0.1,
            "max_tokens": 50,
            "top_p": 1.0,
            "stream": False,
            "stop": ["\n\n"],
        }
    ]
    assert (response.content, response.model) == ("hello", "qwen-7b")
    assert (response.input_tokens, response.output_tokens) == (9, 2)
    assert response.finish_reason == "length"


@pytest.mark.asyncio
async def test_stream_yields_chunks_in_order_then_usage() -> None:
    """Test that streamed deltas arrive in order and the last item is the usage.

    **Why this test is important:**
      - The answer streams to the user token by token; a reordered or dropped delta
        corrupts it, and a missing usage under-bills the ledger.

    **What it tests:**
      - ``stream_with_usage`` yields ``"Hel"``, ``"lo"``, ``"!"`` then a ``StreamUsage``
        with the final chunk's counts and a ``stop`` reason; ``stream`` yields only the
        text
      - the request asks for the usage (``stream_options.include_usage``)
    """
    sent: list[dict[str, object]] = []
    body = _sse(
        _delta("Hel"),
        _delta("lo"),
        _delta("!", finish="stop"),
        {
            "model": "qwen-7b",
            "choices": [],
            "usage": {"prompt_tokens": 4, "completion_tokens": 3},
        },
    )

    def handler(request: httpx.Request) -> httpx.Response:
        """Record the body and answer with the canned stream.

        Args:
            request: The HTTP request.

        Returns:
            The streaming response.

        """
        sent.append(cast("dict[str, object]", json.loads(request.content)))
        return httpx.Response(200, content=body)

    provider = _provider(handler)
    items = await _collect(await provider.stream_with_usage(_MESSAGES))
    text = [t async for t in await provider.stream(_MESSAGES)]

    assert items == [
        "Hel",
        "lo",
        "!",
        StreamUsage(
            model="qwen-7b", input_tokens=4, output_tokens=3, finish_reason="stop"
        ),
    ]
    assert text == ["Hel", "lo", "!"]
    assert sent[0]["stream_options"] == {"include_usage": True}


@pytest.mark.asyncio
async def test_a_stalled_stream_times_out_with_a_coded_error() -> None:
    """Test that a stream that goes silent fails with ``TIMEOUT`` after the stall timeout.

    **Why this test is important:**
      - A server that holds the connection open and sends nothing would hang the user's
        answer (and a worker slot) until the long read timeout; the stall bound releases
        it.

    **What it tests:**
      - after one delta, a server that sends nothing more raises ``AppError(TIMEOUT)``
        within the 0.05 s stall timeout; the delta before the stall was delivered
    """

    async def stalled() -> AsyncIterator[bytes]:
        """Send one chunk, then stall.

        Yields:
            The first chunk.

        """
        yield f"data: {json.dumps(_delta('Hi'))}\n\n".encode()
        await asyncio.sleep(10)

    def handler(_request: httpx.Request) -> httpx.Response:
        """Answer with the stalling stream.

        Args:
            _request: Unused.

        Returns:
            The streaming response.

        """
        return httpx.Response(200, content=stalled())

    received: list[str | StreamUsage] = []
    stream = await _provider(handler, stall=0.05).stream_with_usage(_MESSAGES)
    with pytest.raises(AppError) as raised:
        await asyncio.wait_for(_drain_into(received, stream), timeout=2)

    assert raised.value.code is ErrorCode.TIMEOUT
    assert received == ["Hi"]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("status", "code"),
    [
        (429, ErrorCode.UNAVAILABLE),
        (500, ErrorCode.UNAVAILABLE),
        (401, ErrorCode.INTERNAL),
    ],
)
async def test_status_errors_are_coded(status: int, code: ErrorCode) -> None:
    """Test that a failed call raises a coded error on both paths.

    **Why this test is important:**
      - The fallback and retry decorators act on the code: a throttled server is retried,
        a bad key is not.

    **What it tests:**
      - ``complete`` and ``stream_with_usage`` both raise ``code`` for ``status``

    Args:
        status: The HTTP status.
        code: The code it maps to.

    """
    provider = _provider(lambda _request: httpx.Response(status, json={}))

    with pytest.raises(AppError) as unary:
        _ = await provider.complete(_MESSAGES)
    with pytest.raises(AppError) as streamed:
        _ = await _collect(await provider.stream_with_usage(_MESSAGES))

    assert unary.value.code is code
    assert streamed.value.code is code


def test_factory_builds_the_kind_and_requires_a_model() -> None:
    """Test that the ``openai_compatible`` kind gets its host, timeout and key.

    It needs a model.

    **Why this test is important:**
      - A provider with no model fails only on the first request, as an opaque 4xx; a
        missing key or the 5 s httpx default fails every real call.

    **What it tests:**
      - the client is built on ``host`` with ``timeout_seconds`` and the bearer key, and
        the key is not in the config's repr; an empty model raises ``AppValueError``
    """
    config = LlmConfig(
        kind=LlmKind.OPENAI_COMPATIBLE,
        host="http://llm:8000",
        model="qwen",
        timeout_seconds=30.0,
        api_key="sk-test",
    )

    with patch("techai_webutils.clients.llm.builder.httpx.AsyncClient") as client:
        provider = new_llm_from_config(config)

    assert isinstance(provider, OpenAICompatibleLlmProvider)
    assert provider.model_name() == "qwen"
    assert client.call_args.kwargs == {
        "base_url": "http://llm:8000",
        "timeout": 30.0,
        "headers": {"Authorization": "Bearer sk-test"},
    }
    assert "sk-test" not in repr(config)
    with pytest.raises(AppValueError):
        _ = new_llm_from_config(LlmConfig(kind=LlmKind.OPENAI_COMPATIBLE))
