"""Behavioural tests for the async client resilience stack (new_client_stack_from_config).

Why this suite matters:
    - The direct-SDK async clients (Bedrock, Ollama, S3, SQS) get their timeout / retry /
      circuit-breaker / bulkhead protection from this composed stack; each layer must behave
      correctly and the disabled config must yield a bare passthrough.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock, create_autospec

import pytest

from techai_webutils.clients.decorators.proxy import (
    ClientStackConfig,
    RateLimitProxy,
    new_client_stack_from_config,
)
from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitBreaker,
    CircuitOpenError,
)
from techai_webutils.foundation.resilience.classify import is_transient

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


def _client(**do_config: object) -> MagicMock:
    """Return a mock client whose async ``do`` is an ``AsyncMock`` configured with ``do_config``.

    The resilience proxies forward ``getattr(client, "do")`` and branch on
    ``inspect.iscoroutinefunction`` (True for ``AsyncMock``), so this stands in for the direct-SDK
    client the stack wraps. ``do.await_count`` gives the invocation count the old fake tracked by hand.
    """
    client = MagicMock()
    client.do = AsyncMock()
    client.do.configure_mock(**do_config)
    return client


def _sleeping_do(seconds: float) -> Callable[..., Awaitable[str]]:
    """Return an async ``do`` side effect that sleeps ``seconds`` then returns ``"ok"``."""

    async def _do(*_args: object, **_kwargs: object) -> str:
        await asyncio.sleep(seconds)
        return "ok"

    return _do


@pytest.mark.asyncio
async def test_client_stack_disabled_config_is_passthrough() -> None:
    """A disabled config returns the bare client (backwards-compatible path).

    What it tests: the composer returns the wrapped object itself and calling it invokes the
    method directly with no resilience layers.
    """
    fake = _client(return_value="ok")
    stacked = new_client_stack_from_config(fake, "t", ClientStackConfig(enabled=False))
    assert stacked is fake
    assert await stacked.do() == "ok"


@pytest.mark.asyncio
async def test_client_stack_retries_transient_then_succeeds() -> None:
    """A Retryable transient failure is retried by the stack's Retry layer.

    What it tests: with retry enabled, a call failing once transiently then succeeding is
    invoked twice and returns success.
    """
    fake = _client(side_effect=[AppError(ErrorCode.UNAVAILABLE, "transient"), "ok"])
    cfg = ClientStackConfig(
        retry_enabled=True,
        retry_max_attempts=3,
        bulkhead_max_concurrent=None,
        timeout_seconds=None,
    )
    stacked = new_client_stack_from_config(fake, "t", cfg)
    assert await stacked.do() == "ok"
    assert fake.do.await_count == 2


@pytest.mark.asyncio
async def test_client_stack_circuit_opens_after_threshold() -> None:
    """The injected circuit breaker trips after repeated failures and then fails fast.

    What it tests: once the breaker opens, a call raises CircuitOpenError without invoking the
    wrapped method.
    """
    fake = _client(
        side_effect=AppError(ErrorCode.UNAVAILABLE, "transient")
    )  # always fails
    cb = CircuitBreaker(failure_threshold=2)
    cfg = ClientStackConfig(bulkhead_max_concurrent=None, timeout_seconds=None)
    stacked = new_client_stack_from_config(fake, "t", cfg, circuit_breaker=cb)

    open_hit = False
    for _ in range(6):
        try:
            await stacked.do()
        except CircuitOpenError:
            open_hit = True
            break
        except AppError:
            pass
    assert open_hit, "breaker should open after repeated failures"

    calls_at_open = fake.do.await_count
    with pytest.raises(CircuitOpenError):
        await stacked.do()
    assert fake.do.await_count == calls_at_open, (
        "open breaker must not invoke the wrapped method"
    )


@pytest.mark.asyncio
async def test_client_stack_bulkhead_limits_concurrency() -> None:
    """The bulkhead bounds concurrent in-flight calls.

    What it tests: with max_concurrent=1, two concurrent calls never overlap (peak in-flight 1).
    """
    in_flight = 0
    peak = 0

    async def _tracking_do(*_args: object, **_kwargs: object) -> str:
        """Record peak concurrency while sleeping, so the bulkhead's serialization is observable."""
        nonlocal in_flight, peak
        in_flight += 1
        peak = max(peak, in_flight)
        try:
            await asyncio.sleep(0.05)
            return "ok"
        finally:
            in_flight -= 1

    fake = _client(side_effect=_tracking_do)
    cfg = ClientStackConfig(bulkhead_max_concurrent=1, timeout_seconds=None)
    stacked = new_client_stack_from_config(fake, "t", cfg)
    await asyncio.gather(stacked.do(), stacked.do())
    assert peak == 1


@pytest.mark.asyncio
async def test_client_stack_preserves_configured_timeout() -> None:
    """The Timeout layer applies the configured deadline verbatim, never a shorter default.

    What it tests: a fast call under a long (300s) timeout succeeds (not shortened), while a slow
    call under a short timeout is cut off.
    """
    fast = _client(side_effect=_sleeping_do(0.01))
    fast_stacked = new_client_stack_from_config(
        fast, "t", ClientStackConfig(timeout_seconds=300.0, bulkhead_max_concurrent=None)
    )
    assert await fast_stacked.do() == "ok"

    slow = _client(side_effect=_sleeping_do(0.2))
    slow_stacked = new_client_stack_from_config(
        slow, "t", ClientStackConfig(timeout_seconds=0.05, bulkhead_max_concurrent=None)
    )
    with pytest.raises((TimeoutError, AppError)):
        await slow_stacked.do()


class _Permanent:
    """Async fake that always raises a PERMANENT (non-transient) domain error; counts calls."""

    def __init__(self) -> None:
        """Start the call counter at zero."""
        self.calls = 0

    async def do(self) -> str:
        """Increment the counter and raise a permanent NotFound error."""
        self.calls += 1
        raise AppError(ErrorCode.NOT_FOUND, "gone")


@pytest.mark.asyncio
async def test_client_stack_permanent_error_does_not_trip_breaker() -> None:
    """A permanent domain error must NOT trip the circuit breaker (only transient infra failures do).

    Why this test is important:
        - A breaker that trips on ordinary business outcomes (NotFound, validation) fails healthy
          callers; the breaker must be error-aware, mirroring the Go gobreaker IsSuccessful.

    What it tests:
        - With a classifying breaker (is_failure=is_transient), repeated permanent AppErrors never open
          the circuit, so every call still reaches the wrapped method.
    """
    fake = _Permanent()
    cb = CircuitBreaker(failure_threshold=2, is_failure=is_transient)
    cfg = ClientStackConfig(bulkhead_max_concurrent=None, timeout_seconds=None)
    stacked = new_client_stack_from_config(fake, "t", cfg, circuit_breaker=cb)

    for _ in range(6):
        with pytest.raises(AppError):
            await stacked.do()
    assert fake.calls == 6, "permanent errors must not trip the breaker"


def test_client_stack_wrap_order_tracing_outermost_logging_innermost() -> None:
    """The composed client stack nests Tracing -> Logging (Logging innermost), per ARCHITECTURE.md#decorator-order.

    Why this test is important:
        - The tracing span must bracket the logging seam; before the Go builders had
          this inverted (Logging outermost). This locks the Python composer's order so a reorder
          of the LoggingProxy/TracingProxy composition fails the test.

    What it tests:
        - With only logging + tracing enabled, the outermost proxy is a TracingProxy wrapping a
          LoggingProxy wrapping the target — so Logging is the innermost seam.
    """

    class _Target:
        async def do(self) -> str:
            return "ok"

    target = _Target()
    stacked = new_client_stack_from_config(
        target,
        "t",
        ClientStackConfig(
            timeout_seconds=None, retry_enabled=False, bulkhead_max_concurrent=None
        ),
    )

    assert type(stacked).__name__ == "TracingProxy", (
        "Tracing must be outermost of the trio"
    )
    inner = stacked._wrapped  # noqa: SLF001 — structural assertion of the composed nesting
    assert type(inner).__name__ == "LoggingProxy", (
        "Logging must sit inside Tracing (innermost)"
    )
    assert inner._wrapped is target  # noqa: SLF001 — LoggingProxy wraps the target directly


@pytest.mark.asyncio
async def test_rate_limit_proxy_gates_each_call_through_the_limiter() -> None:
    """RateLimitProxy awaits a token from the RateLimiter before each async call.

    Why this test is important:
        - The RateLimit layer (Job/Controller stacks) must smooth bursts to the
          configured rate; a proxy that forgot to await the limiter would let a burst through.

    What it tests:
        - Two calls through the proxy each await the limiter once, and both reach the target.
    """

    class _Target:
        def __init__(self) -> None:
            self.calls = 0

        async def do(self) -> str:
            self.calls += 1
            return "ok"

    limiter = create_autospec(RateLimiter, instance=True)
    limiter.allow.return_value = True
    target = _Target()
    proxied = cast("_Target", RateLimitProxy(target, limiter))

    assert await proxied.do() == "ok"
    assert await proxied.do() == "ok"
    assert limiter.wait.await_count == 2, "each call must await a rate-limit token"
    assert target.calls == 2
