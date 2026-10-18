"""Tests for the client tier: ``ClientBase`` (Go ``clients/client``)."""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.client import ClientBase, handler, producer, tier
from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.retrier import Retrier
from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations, Deps, Layer

if TYPE_CHECKING:
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


class _Queue(Protocol):
    """The client contract under decoration."""

    async def send(self, body: str) -> str:
        """Send one message."""
        ...


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
async def test_client_base_exec_once_handler_and_producer(observed: Observed) -> None:
    """``exec`` retries, ``exec_once`` never does, and a handler base drops the call-side layers.

    Why this test is important:
      - A message handler is retried by the broker's redelivery; a client retry on top would
        multiply deliveries.

    What it tests:
      - ``exec`` retries a flap; ``exec_once`` raises it; a wire base with ``handler()`` does
        not retry and observes in CONSUMER spans; ``producer()`` opens PRODUCER spans.
    """
    retrier = _retrier()
    decorations = Decorations.fixed(
        Deps(observability=observed.observability, retrier=retrier)
    )
    base = ClientBase(_Queue, decorations, "queue")
    flaky = AsyncMock(side_effect=[AppError(ErrorCode.UNAVAILABLE, "flap"), "ok"])

    assert await base.exec("send", flaky) == "ok"
    with pytest.raises(AppError):
        await base.exec_once(
            "send", AsyncMock(side_effect=AppError(ErrorCode.UNAVAILABLE, "x"))
        )
    handlers = ClientBase.wire(decorations, "queue.handler", handler())
    retrier.retry.reset_mock()
    assert handlers.name == "queue.handler"
    assert await handlers.exec("orders", AsyncMock(return_value=1)) == 1
    retrier.retry.assert_not_awaited()
    assert observed.spans[-1][:2] == ("queue.handler.orders", SpanKind.CONSUMER)
    bus = ClientBase.wire(
        Decorations.fixed(Deps(observability=observed.observability)), "bus", producer()
    )
    await bus.exec_once("orders", AsyncMock())
    assert observed.spans[-1][:2] == ("bus.orders", SpanKind.PRODUCER)


@pytest.mark.asyncio
async def test_client_base_applies_the_clients_tier_per_operation(
    observed: Observed,
) -> None:
    """The decoration config's ``clients`` tier switches a client's layers by name.

    Why this test is important:
      - Decorators are configured, not hard-wired, down to one operation.

    What it tests:
      - With tracing off for ``quiet``, its calls open no span while ``loud``'s do.
    """
    config = decoration.from_mapping({
        "clients": {"overrides": {"quiet.send": {"tracing": False}}}
    })
    decorations = Decorations(config, Collaborators(observability=observed.observability))
    quiet = ClientBase(_Queue, decorations, "quiet")
    loud = ClientBase(_Queue, decorations, "loud")

    assert await quiet.exec("send", AsyncMock(return_value=1)) == 1
    assert await loud.exec("send", AsyncMock(return_value=2)) == 2
    assert [name for name, _, _ in observed.spans] == ["loud.send"]
