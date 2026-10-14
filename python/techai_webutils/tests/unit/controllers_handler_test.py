"""Tests for the controller tier: ``ControllerBase`` and ``lay_out`` (Go ``transport.Base`` / ``Interceptors``)."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.controllers import ControllerBase, tier
from techai_webutils.controllers.interceptors import lay_out
from techai_webutils.core.errors.errors import (
    AppError,
    ErrorCode,
    ForbiddenError,
    InternalError,
)
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.core.interfaces.tracer import SpanKind
from techai_webutils.foundation.decorate import (
    Collaborators,
    Decorations,
    Deps,
    Layer,
    Observability,
    OpMiddleware,
    exec_op,
)

if TYPE_CHECKING:
    from conftest import Observed


def test_tier_order_matches_go() -> None:
    """The controller tier's layers, outermost first, are Go's, with SERVER spans.

    Why this test is important:
      - A shed, denied or panicking request must still be observed.

    What it tests:
      - tracing → metrics → logging → recovery → rate limit → bulkhead → auth → timeout; the
        tier is the edge, so it logs failures above Debug.
    """
    assert tier().order == (
        Layer.TRACING,
        Layer.METRICS,
        Layer.LOGGING,
        Layer.RECOVERY,
        Layer.RATE_LIMIT,
        Layer.BULKHEAD,
        Layer.AUTH,
        Layer.TIMEOUT,
    )
    assert tier().span_kind is SpanKind.SERVER
    assert tier().edge is True


@pytest.mark.asyncio
async def test_handler_runs_through_the_tier(observed: Observed) -> None:
    """A handler runs as its operation in a SERVER span; failures are coded.

    Why this test is important:
      - Every REST or RPC handler is decorated the same way, configured per controller.

    What it tests:
      - The response is returned in a ``search.Query`` SERVER span; a denied action raises
        ``ForbiddenError``; a crash is coded ``InternalError`` and still observed.
    """
    deps = Deps(observability=observed.observability)
    base = ControllerBase("search", deps)
    query = base.handler("Query", AsyncMock(side_effect=lambda req: f"answer:{req}"))

    assert await query("q") == "answer:q"
    assert observed.spans[0][:2] == ("search.Query", SpanKind.SERVER)
    assert base.name == "search"

    denied = ControllerBase("search", Deps(auth=AsyncMock(return_value=False)))
    with pytest.raises(ForbiddenError):
        await denied.exec("Delete", AsyncMock())
    with pytest.raises(InternalError):
        await base.exec("Crash", AsyncMock(side_effect=RuntimeError("x")))
    assert len(observed.span_errors) == 1


@pytest.mark.asyncio
async def test_handler_failures_are_logged_as_the_edge(observed: Observed) -> None:
    """A handler failure is logged at Warning for a client fault and at Error otherwise.

    Why this test is important:
      - A REST handler's failure is logged by no one else: logged at Debug, like an inner
        tier's, a 500 would leave no trace in a production log.

    What it tests:
      - A denial (403) logs ``controller.Delete failed`` at Warning; a crash (500) logs
        ``controller.Crash failed`` at Error; neither is logged at Debug.
    """
    auth = AsyncMock(return_value=False)
    base = ControllerBase("search", Deps(observability=observed.observability, auth=auth))

    with pytest.raises(ForbiddenError):
        await base.exec("Delete", AsyncMock())
    auth.return_value = True
    with pytest.raises(InternalError):
        await base.exec("Crash", AsyncMock(side_effect=RuntimeError("x")))

    assert observed.logger.warning.call_args.args == ("controller.Delete failed",)
    assert observed.logger.error.call_args.args == ("controller.Crash failed",)
    debug = [c.args[0] for c in observed.logger.debug.call_args_list]
    assert debug == ["controller.Delete", "controller.Crash"]


@pytest.mark.asyncio
async def test_rate_limit_rejects_resource_exhausted() -> None:
    """A request over the rate is rejected RESOURCE_EXHAUSTED before the handler runs.

    Why this test is important:
      - A shed request must be the retry-later status, never a generic error.

    What it tests:
      - A refusing limiter raises an ``AppError`` coded RESOURCE_EXHAUSTED; the handler is not
        run.
    """
    limiter = MagicMock(spec=RateLimiter)
    limiter.allow.return_value = False
    handler = AsyncMock()
    base = ControllerBase("api", Deps(rate_limiter=limiter))

    with pytest.raises(AppError) as caught:
        await base.handler("Get", handler)("req")
    assert caught.value.code is ErrorCode.RESOURCE_EXHAUSTED
    handler.assert_not_awaited()
    assert (
        ControllerBase.from_decorations("api", Decorations(None, Collaborators())).name
        == "api"
    )


def test_lay_out_splits_runs_around_native_layers_and_authn() -> None:
    """``lay_out`` stands native interceptors in for their layers and authn before auth.

    Why this test is important:
      - Every server framework lays the tier out the same way (Go ``transport.Interceptors``).

    What it tests:
      - With native tracing only, the result is [native tracing, run(metrics…rate limit),
        authn, run(auth, timeout)]; runs with no collaborators add nothing.
    """
    calls: list[tuple[str, OpMiddleware, OpMiddleware]] = []

    def native(layer: Layer) -> list[str] | None:
        return ["native-tracing"] if layer is Layer.TRACING else None

    def adapt(unary: OpMiddleware, stream: OpMiddleware) -> str:
        calls.append(("run", unary, stream))
        return f"run{len(calls)}"

    limiter = MagicMock(spec=RateLimiter)
    deps = Deps(
        observability=Observability(logger=MagicMock()),
        auth=AsyncMock(return_value=True),
        rate_limiter=limiter,
        timeout=1.0,
    )

    assert lay_out("api", deps, native, adapt, ["authn"]) == [
        "native-tracing",
        "run1",
        "authn",
        "run2",
    ]
    assert lay_out("api", Deps(skip_recovery=True), native, adapt) == ["native-tracing"]


@pytest.mark.asyncio
async def test_lay_out_streams_get_no_timeout() -> None:
    """A run's stream middleware omits the timeout its unary middleware has.

    Why this test is important:
      - A long-lived stream must outlive any per-call budget (Go parity).

    What it tests:
      - With a 10 ms timeout, a 50 ms operation times out through the unary middleware and
        completes through the stream middleware.
    """
    runs: list[tuple[OpMiddleware, OpMiddleware]] = []

    def adapt(unary: OpMiddleware, stream: OpMiddleware) -> int:
        runs.append((unary, stream))
        return len(runs)

    lay_out("api", Deps(timeout=0.01, skip_recovery=True), lambda _: None, adapt)
    [(unary, stream)] = runs

    async def slow() -> str:
        await asyncio.sleep(0.05)
        return "done"

    with pytest.raises(AppError):
        await exec_op(unary, "Op", slow)
    assert await exec_op(stream, "Op", slow) == "done"
