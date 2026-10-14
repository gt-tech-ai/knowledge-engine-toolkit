"""Tests for the client tier: ``ClientBase`` and ``decorate`` (Go ``clients/client``)."""

from __future__ import annotations

from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.client import (
    ClientBase,
    decorate,
    decorate_from_decorations,
    tier,
)
from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations, Deps, Layer

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from conftest import Observed


def _retrier() -> MagicMock:
    """Return a Retrier mock that retries one transient failure."""
    retrier = MagicMock(spec=Retrier)

    async def retry(op: object) -> object:
        try:
            return await op()  # type: ignore[operator]
        except AppError as exc:
            if not exc.is_transient:
                raise
            return await op()  # type: ignore[operator]

    retrier.retry = AsyncMock(side_effect=retry)
    return retrier


class _Search:
    """The client under decoration: an async call, a stream and a sync helper."""

    def __init__(self) -> None:
        """Start with no calls."""
        self.calls = 0
        self.label = "search"

    async def query(self, text: str) -> str:
        """Fail UNAVAILABLE once, then answer."""
        self.calls += 1
        if self.calls == 1:
            raise AppError(ErrorCode.UNAVAILABLE, "flap")
        return text.upper()

    async def ingest(self, text: str) -> str:
        """Fail UNAVAILABLE every time (it must not be retried)."""
        self.calls += 1
        raise AppError(ErrorCode.UNAVAILABLE, text)

    async def stream(self, text: str) -> AsyncIterator[str]:
        """Yield the text's characters."""
        for ch in text:
            yield ch

    def size(self) -> int:
        """Return a constant (a sync method)."""
        return 3


def test_tier_order_matches_go() -> None:
    """The client tier's layers, outermost first, are Go's, with CLIENT spans.

    Why this test is important:
      - Each retry attempt must pass the breaker and get its own deadline and span.

    What it tests:
      - bulkhead → retry → circuit breaker → timeout → tracing → metrics → logging, kind CLIENT.
    """
    assert tier().order == (
        Layer.BULKHEAD,
        Layer.RETRY,
        Layer.CIRCUIT_BREAKER,
        Layer.TIMEOUT,
        Layer.TRACING,
        Layer.METRICS,
        Layer.LOGGING,
    )
    assert tier().span_kind is SpanKind.CLIENT


@pytest.mark.asyncio
async def test_decorate_retries_coroutines_and_passes_the_rest_through(
    observed: Observed,
) -> None:
    """Coroutine methods run through the tier; streams and sync methods pass through.

    Why this test is important:
      - A caller programs to the client it passed in, so the proxy must keep every method
        working, while every I/O call gets the client tier.

    What it tests:
      - ``query`` is retried past one flap with a CLIENT span per attempt; ``ingest`` (listed in
        ``once``) is not retried; the stream, the sync method and attributes pass through.
    """
    inner = _Search()
    deps = Deps(observability=observed.observability, retrier=_retrier())
    client = decorate(inner, "search", deps, once=("ingest",))

    assert await client.query("hi") == "HI"
    assert inner.calls == 2
    assert [(name, kind) for name, kind, _ in observed.spans] == [
        ("search.query", SpanKind.CLIENT),
        ("search.query", SpanKind.CLIENT),
    ]
    inner.calls = 0
    with pytest.raises(AppError):
        await client.ingest("x")
    assert inner.calls == 1
    assert [ch async for ch in client.stream("ab")] == ["a", "b"]
    assert client.size() == 3
    assert client.label == "search"


@pytest.mark.asyncio
async def test_client_base_exec_once_and_handler(observed: Observed) -> None:
    """``exec`` retries, ``once`` never does, and a handler base drops the call-side layers.

    Why this test is important:
      - A message handler is retried by the broker's redelivery; a client retry on top would
        multiply deliveries.

    What it tests:
      - ``exec`` retries a flap; ``once`` raises it; ``handler`` is named ``<name>.handler``
        and does not retry, while keeping observability in CONSUMER spans; ``producer`` opens
        PRODUCER spans.
    """
    retrier = _retrier()
    base = ClientBase(
        "queue", Deps(observability=observed.observability, retrier=retrier)
    )
    flaky = AsyncMock(side_effect=[AppError(ErrorCode.UNAVAILABLE, "flap"), "ok"])

    assert await base.exec("Send", flaky) == "ok"
    with pytest.raises(AppError):
        await base.once(
            "Send", AsyncMock(side_effect=AppError(ErrorCode.UNAVAILABLE, "x"))
        )
    handler = ClientBase.handler(
        "queue", Deps(observability=observed.observability, retrier=retrier)
    )
    retrier.retry.reset_mock()
    assert handler.name == "queue.handler"
    assert await handler.exec("Handle", AsyncMock(return_value=1)) == 1
    retrier.retry.assert_not_awaited()
    assert observed.spans[-1][:2] == ("queue.handler.Handle", SpanKind.CONSUMER)
    producer = ClientBase.producer("bus", Deps(observability=observed.observability))
    await producer.once("orders", AsyncMock())
    assert observed.spans[-1][:2] == ("bus.orders", SpanKind.PRODUCER)


@pytest.mark.asyncio
async def test_decorate_from_decorations_applies_the_clients_tier(
    observed: Observed,
) -> None:
    """The decoration config's ``clients`` tier switches a client's layers by name.

    Why this test is important:
      - Decorators are configured, not hard-wired.

    What it tests:
      - With tracing off for ``quiet``, its calls open no span while ``loud``'s do.
    """
    config = decoration.from_mapping({
        "clients": {"overrides": {"quiet": {"tracing": False}}}
    })
    decorations = Decorations(config, Collaborators(observability=observed.observability))

    class _Echo:
        async def echo(self, x: int) -> int:
            return x

    quiet = decorate_from_decorations(_Echo(), "quiet", decorations)
    loud = decorate_from_decorations(_Echo(), "loud", decorations)

    assert await quiet.echo(1) == 1
    assert await loud.echo(2) == 2
    assert [name for name, _, _ in observed.spans] == ["loud.echo"]
    assert ClientBase.from_decorations("x", decorations).name == "x"


@pytest.mark.asyncio
async def test_decorate_keeps_the_clients_async_lifecycle() -> None:
    """``async with`` on a decorated client opens and closes the client and keeps the tier.

    Why this test is important:
      - ``async with`` looks ``__aenter__`` up on the type, past ``__getattr__``; a proxy that
        did not forward it would fail, or hand back the undecorated client.

    What it tests:
      - Entering the decorated client enters the client and yields the proxy, whose calls still
        run through the tier; leaving it leaves the client with the block's exception info.
    """
    inner = MagicMock()
    inner.__aenter__ = AsyncMock(return_value=inner)
    inner.__aexit__ = AsyncMock(return_value=None)
    inner.ping = AsyncMock(return_value="pong")
    client = decorate(inner, "pinger", Deps())

    async with client as opened:
        assert opened is client
        assert await opened.ping() == "pong"

    inner.__aenter__.assert_awaited_once()
    inner.__aexit__.assert_awaited_once_with(None, None, None)
