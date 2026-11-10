"""Unit tests for the usage-recording LLM decorator.

The module is ``clients/decorators/usage_tap.py``.
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncGenerator
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock, create_autospec

import pytest

from techai_webutils.clients.decorators.usage_tap import tap_usage
from techai_webutils.core.interfaces.llm import LLMProvider, LLMResponse, StreamUsage
from techai_webutils.core.interfaces.model_gateway import (
    RouteDecision,
    set_route_decision,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator


def _recorded(record: MagicMock) -> list[StreamUsage]:
    """Return every usage handed to the ``record`` callback, in order.

    Args:
        record: The tap's callback.

    Returns:
        Every usage handed to the ``record`` callback, in order.

    """
    return [call.args[0] for call in record.call_args_list]


@pytest.mark.asyncio
async def test_unary_usage_is_recorded_under_the_routes_catalog_id() -> None:
    """A completion's usage is recorded with the catalog id the gateway routed it to.

    Why this test is important:
      - A consumer stores the recorded model with the answer (an answer cache, an audit
        record); it must name the catalog model, not the endpoint's provider model id.

    What it tests:
      - the response passes through unchanged; the recorded usage carries its tokens and
        the route decision's catalog id.
    """
    record = MagicMock()
    provider = cast("LLMProvider", create_autospec(LLMProvider, instance=True))
    response = LLMResponse("answer", "us.anthropic.claude-sonnet-4-6", 900, 40, "stop")

    def complete(*_: object) -> LLMResponse:
        """Route like a gateway: record the decision, then answer.

        Returns:
            The gateway's answer.

        """
        set_route_decision(RouteDecision(model_id="anthropic/claude-sonnet-4-6"))
        return response

    provider.complete = AsyncMock(side_effect=complete)

    assert await tap_usage(provider, record).complete([]) is response
    assert _recorded(record) == [
        StreamUsage("anthropic/claude-sonnet-4-6", 900, 40, "stop")
    ]


@pytest.mark.asyncio
async def test_streamed_usage_is_recorded_and_only_text_yielded() -> None:
    """A stream yields its text and records the terminal usage it reports.

    Why this test is important:
      - A text-only caller must never see the usage item as a token, and the consumer must
        still learn which model answered and how many tokens it used.

    What it tests:
      - ``stream`` yields the text deltas from ``stream_with_usage`` and records its
        ``StreamUsage`` (the provider's model id without a route decision).
    """
    record = MagicMock()
    usage = StreamUsage("qwen2.5:7b", 300, 20, "stop")

    async def items() -> AsyncIterator[str | StreamUsage]:
        """Stream two deltas, then the terminal usage.

        Yields:
            The stream's deltas and usage.

        """
        await asyncio.sleep(0)
        yield "Q3 "
        yield "was 12."
        yield usage

    provider = cast("LLMProvider", create_autospec(LLMProvider, instance=True))
    provider.stream_with_usage = AsyncMock(return_value=items())

    text = [t async for t in await tap_usage(provider, record).stream([])]

    assert text == ["Q3 ", "was 12."]
    assert _recorded(record) == [usage]


@pytest.mark.asyncio
async def test_an_abandoned_stream_closes_the_provider_stream() -> None:
    """Closing the tap's stream early closes the provider's stream beneath it.

    Why this test is important:
      - A consumer closes a stream the client stopped reading so the model stops
        generating; a provider stream left to the garbage collector keeps its connection
        (and its billing) open.

    What it tests:
      - after one delta, ``aclose()`` on the tap's iterator runs the provider stream's
        cleanup.
    """
    closed: list[bool] = []

    async def items() -> AsyncIterator[str | StreamUsage]:
        """Stream deltas until closed, recording the close.

        Yields:
            The stream's deltas and usage.

        """
        await asyncio.sleep(0)
        try:
            yield "Q3 "
            yield "was 12."
        finally:
            closed.append(True)

    provider = cast("LLMProvider", create_autospec(LLMProvider, instance=True))
    provider.stream_with_usage = AsyncMock(return_value=items())
    stream = await tap_usage(provider, MagicMock()).stream([])
    assert isinstance(stream, AsyncGenerator)

    assert await anext(stream) == "Q3 "
    await stream.aclose()

    assert closed == [True]


@pytest.mark.asyncio
async def test_streamed_usage_names_the_route_decided_at_the_end_of_the_stream() -> None:
    """A stream's usage names the catalog model the gateway decided once the stream ended.

    Why this test is important:
      - A routed gateway records which catalog model served a stream only after yielding
        its usage; recording on the usage item would name the previous call's model.

    What it tests:
      - with the rewrite's decision current and the generate decision set only after the
        usage item, the recorded usage names the generate route's catalog id.
    """
    record = MagicMock()
    set_route_decision(
        RouteDecision(route="rewrite", model_id="anthropic/claude-haiku-4-5")
    )

    async def items() -> AsyncIterator[str | StreamUsage]:
        """Stream a delta and the usage, then decide the route like a routed gateway.

        Yields:
            The stream's deltas and usage.

        """
        await asyncio.sleep(0)
        yield "Q3 was 12."
        yield StreamUsage("qwen2.5:7b", 300, 20, "stop")
        set_route_decision(
            RouteDecision(route="generate", model_id="anthropic/claude-sonnet-4-6")
        )

    provider = cast("LLMProvider", create_autospec(LLMProvider, instance=True))
    provider.stream_with_usage = AsyncMock(return_value=items())

    text = [t async for t in await tap_usage(provider, record).stream([])]

    assert text == ["Q3 was 12."]
    assert [u.model for u in _recorded(record)] == ["anthropic/claude-sonnet-4-6"]


@pytest.mark.asyncio
async def test_the_tap_is_the_provider_it_wraps() -> None:
    """The tap is an ``LLMProvider`` that forwards the rest of the provider surface.

    Why this test is important:
      - Every caller and decorator outside the tap speaks to it as the provider: a caller
        of ``stream_with_usage`` must still get the usage item (and the consumer its
        record), ``model_name`` must name the wrapped model, and entering or leaving the
        tap must open and release the provider's client.

    What it tests:
      - the tap is an ``LLMProvider``; ``stream_with_usage`` yields every item, the usage
        included, and records the usage; ``model_name`` and the async context are the
        inner provider's.
    """
    record = MagicMock()
    usage = StreamUsage("qwen2.5:7b", 300, 20, "stop")

    async def items() -> AsyncIterator[str | StreamUsage]:
        """Stream one delta, then the terminal usage.

        Yields:
            The stream's delta and usage.

        """
        await asyncio.sleep(0)
        yield "Q3 was 12."
        yield usage

    provider = cast("LLMProvider", create_autospec(LLMProvider, instance=True))
    provider.stream_with_usage = AsyncMock(return_value=items())
    provider.model_name = lambda: "qwen2.5:7b"
    exit_ = AsyncMock(return_value=None)
    provider.__aexit__ = exit_
    tap = tap_usage(provider, record)

    assert isinstance(tap, LLMProvider)
    assert [i async for i in await tap.stream_with_usage([])] == ["Q3 was 12.", usage]
    assert _recorded(record) == [usage]
    assert tap.model_name() == "qwen2.5:7b"
    async with tap:
        pass
    exit_.assert_awaited_once()
