"""Unit tests for the client-proxy decorators (Logging/Tracing/Retry/CircuitBreaker).

Covers transparent method/attribute forwarding, decorator-chain composition, and error propagation,
plus the async-first behaviour: an awaited failure is logged/traced, transient errors are retried
(permanent ones are not), and the breaker gates the awaited call. The ``mock_service`` fixture (in
``conftest.py``) is a ``MagicMock`` reproducing the canned returns the proxies forward.
"""

import asyncio
import json
from io import StringIO
from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.decorators.proxy import (
    CircuitBreakerProxy,
    LoggingProxy,
    RetryProxy,
    TracingProxy,
)
from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.foundation.logger.logger import configure_logging
from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitBreaker,
    CircuitOpenError,
)


class _AsyncBoom:
    """Async service whose method always raises a transient ``AppError``; counts invocations."""

    def __init__(self) -> None:
        """Start with zero recorded calls."""
        self.calls = 0

    async def run(self) -> str:
        """Increment the call counter and raise a transient error."""
        self.calls += 1
        raise AppError(ErrorCode.UNAVAILABLE, "boom")


class _AsyncFlaky:
    """Async service that raises a transient ``AppError`` ``fail_times`` times, then returns ``"ok"``."""

    def __init__(self, fail_times: int) -> None:
        """Record how many leading calls should fail transiently."""
        self.calls = 0
        self._fail_times = fail_times

    async def run(self) -> str:
        """Fail transiently for the first ``fail_times`` calls, then succeed."""
        self.calls += 1
        if self.calls <= self._fail_times:
            raise AppError(ErrorCode.TIMEOUT, "flaky")
        return "ok"


class _AsyncPermanentBoom:
    """Async service whose method always raises a PERMANENT (non-transient) ``AppError``; counts calls."""

    def __init__(self) -> None:
        """Start with zero recorded calls."""
        self.calls = 0

    async def run(self) -> str:
        """Increment the call counter and raise a permanent (non-retryable) error."""
        self.calls += 1
        raise AppError(ErrorCode.INVALID_INPUT, "permanent")


class TestLoggingProxy:
    """Test suite for LoggingProxy method forwarding."""

    def test_forwards_method_calls(self, mock_service: MagicMock) -> None:
        """Test that LoggingProxy forwards method calls and returns the correct result.

        **Why this test is important:**
          - The proxy must be transparent; callers should not know they are using a proxy
          - Incorrect forwarding would silently break all service method calls
          - Return values must pass through unmodified for data integrity

        **What it tests:**
          - result equals {"id": "123", "name": "test"} from the wrapped service
        """
        proxy = cast("MagicMock", LoggingProxy(mock_service, logger_name="test"))

        result = proxy.get_item("123")
        assert result == {"id": "123", "name": "test"}

    def test_forwards_attribute_access(self, mock_service: MagicMock) -> None:
        """Test that LoggingProxy exposes the wrapped service's methods as callable attributes.

        **Why this test is important:**
          - Framework code may introspect service objects for available methods
          - Attribute access must work for duck-typing and protocol compliance
          - Broken attribute forwarding would cause AttributeError at runtime

        **What it tests:**
          - proxy.get_item is callable
        """
        proxy = LoggingProxy(mock_service, logger_name="test")

        # Method is accessible
        assert callable(proxy.get_item)

    @pytest.mark.asyncio
    async def test_logs_are_structured_json_with_client_method_and_duration(self) -> None:
        """LoggingProxy emits STRUCTURED JSON (client/method/duration_ms), not printf text.

        **Why this test is important:**
          - The client-stack logging decorator must mirror the Go decorators: every line is
            JSON on the canonical schema, so a single request is followable across services and
            each stage's latency is legible. The prior implementation logged through the stdlib
            logger with printf ``%s`` args ("calling Foo.bar"), producing UNSTRUCTURED lines with
            no ``duration_ms`` and no trace ids — which broke JSON log parsing and left half
            the log unstructured.

        **What it tests:**
          - Every emitted line is valid JSON, and the completion line carries ``client``,
            ``method``, a numeric ``duration_ms``, and level ``debug``.
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)

        class _Svc:
            async def do(self) -> str:
                return "ok"

        proxy = cast("_Svc", LoggingProxy(_Svc(), logger_name="clienttest"))
        assert await proxy.do() == "ok"

        lines = [line for line in output.getvalue().strip().split("\n") if line]
        records = [
            json.loads(line) for line in lines
        ]  # every line MUST parse as JSON (structured)
        complete = [r for r in records if r.get("message") == "client call complete"]
        assert complete, records
        rec = complete[-1]
        assert rec["client"] == "_Svc"
        assert rec["method"] == "do"
        assert isinstance(rec["duration_ms"], (int, float))
        assert rec["level"] == "debug"

    @pytest.mark.asyncio
    async def test_awaits_and_logs_async_failure(self) -> None:
        """Test that LoggingProxy awaits an async method and logs its failure as structured JSON.

        **Why this test is important:**
          - A sync-only proxy wraps the coroutine without awaiting it, so the failure surfaces
            OUTSIDE the proxy and is never logged; the async-first proxy must bracket the await.
          - The failure line must be structured (the inner-seam DEBUG mirror of the Go decorators),
            not a printf ``%s`` stdlib line.

        **What it tests:**
          - awaiting the proxied async method raises the AppError AND a structured JSON
            'client call failed' line (carrying client/method) is emitted.
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)

        proxy = cast("_AsyncBoom", LoggingProxy(_AsyncBoom(), logger_name="asynctest"))
        with pytest.raises(AppError):
            await proxy.run()

        records = [
            json.loads(line) for line in output.getvalue().strip().split("\n") if line
        ]
        failed = [r for r in records if r.get("message") == "client call failed"]
        assert failed, records
        assert failed[-1]["client"] == "_AsyncBoom"
        assert failed[-1]["method"] == "run"


class TestTracingProxy:
    """Test suite for TracingProxy method forwarding."""

    def test_forwards_method_calls(self, mock_service: MagicMock) -> None:
        """Test that TracingProxy forwards method calls and returns the correct result.

        **Why this test is important:**
          - Tracing must not alter the behavior of the wrapped service
          - Return values must pass through unchanged for correctness
          - The proxy pattern relies on transparent delegation for composability

        **What it tests:**
          - result equals {"id": "new", "name": "new-item"} from the wrapped service
        """
        proxy = cast("MagicMock", TracingProxy(mock_service, service_name="test"))

        result = proxy.create_item("new-item")
        assert result == {"id": "new", "name": "new-item"}


class TestDecoratorChain:
    """Test suite for stacked proxy composition and error propagation."""

    def test_chain_forwards_correctly(self, mock_service: MagicMock) -> None:
        """Test that chained proxies forward calls through all layers to the real service.

        **Why this test is important:**
          - Production services use multiple decorators (logging + tracing + metrics)
          - Each layer must forward without intercepting or modifying the call
          - Incorrect chaining would silently drop decorator behavior or break calls

        **What it tests:**
          - result equals {"id": "456", "name": "test"} through Logging -> Tracing -> Service
        """
        # Chain: Logging -> Tracing -> Service
        decorated = cast(
            "MagicMock",
            LoggingProxy(
                TracingProxy(mock_service, service_name="test"),
                logger_name="test",
            ),
        )

        result = decorated.get_item("456")
        assert result == {"id": "456", "name": "test"}

    def test_chain_propagates_errors(self, mock_service: MagicMock) -> None:
        """Test that chained proxies propagate exceptions from the real service.

        **Why this test is important:**
          - Error handling logic depends on receiving the original exception type
          - Swallowed exceptions would cause silent failures and data inconsistency
          - Decorators must not catch exceptions meant for upstream callers

        **What it tests:**
          - RuntimeError with "intentional failure" message propagates through the chain
        """
        decorated = cast(
            "MagicMock",
            LoggingProxy(
                TracingProxy(mock_service, service_name="test"),
                logger_name="test",
            ),
        )

        with pytest.raises(RuntimeError, match="intentional failure"):
            decorated.failing_method()


class TestRetryProxy:
    """Test suite for RetryProxy async retry behavior."""

    @pytest.mark.asyncio
    async def test_retries_async_transient_then_succeeds(self) -> None:
        """Test that RetryProxy retries a transiently-failing async method until it succeeds.

        **Why this test is important:**
          - A sync-only proxy returns the un-awaited coroutine on the first attempt and never
            retries; the async-first proxy must re-invoke the coroutine on transient failure.

        **What it tests:**
          - run() fails transiently twice then returns ``ok``; the proxy returns ``ok`` after 3 calls.
        """
        svc = _AsyncFlaky(fail_times=2)
        proxy = cast("_AsyncFlaky", RetryProxy(svc, max_attempts=3))

        assert await proxy.run() == "ok"
        assert svc.calls == 3

    @pytest.mark.asyncio
    async def test_permanent_async_error_is_not_retried(self) -> None:
        """Test that a permanent (non-transient) async error propagates on the first attempt.

        **Why this test is important:**
          - Only transient failures should be retried; re-invoking a permanent failure wastes the
            attempt budget and delays surfacing the real error.

        **What it tests:**
          - A method raising a non-transient AppError raises immediately after exactly one call.
        """
        svc = _AsyncPermanentBoom()
        proxy = cast("_AsyncPermanentBoom", RetryProxy(svc, max_attempts=3))

        with pytest.raises(AppError):
            await proxy.run()
        assert svc.calls == 1

    @pytest.mark.asyncio
    async def test_waits_out_a_server_pushback_before_retrying(self) -> None:
        """Test that a transient error carrying ``retry_after_ms`` is retried only after that delay.

        **Why this test is important:**
          - The proxy otherwise retries immediately; on a rate-limit pushback that burns every
            attempt inside the window the server said is exhausted.

        **What it tests:**
          - a 300 ms pushback, then success → one sleep of exactly 0.3 s and the result ``ok``
          - a 60 s pushback is capped at ``max_pushback_s`` (2.0 s)
          - a transient error without the detail is retried with no sleep
        """
        delays: list[float] = []

        async def _record_sleep(seconds: float) -> None:
            await asyncio.sleep(0)
            delays.append(seconds)

        svc = MagicMock()
        svc.run = AsyncMock(
            side_effect=[
                AppError(
                    ErrorCode.UNAVAILABLE, "slow", details={"retry_after_ms": "300"}
                ),
                AppError(
                    ErrorCode.UNAVAILABLE, "slow", details={"retry_after_ms": "60000"}
                ),
                AppError(ErrorCode.UNAVAILABLE, "blip"),
                "ok",
            ]
        )
        proxy = cast(
            "MagicMock",
            RetryProxy(svc, max_attempts=4, max_pushback_s=2.0, sleep=_record_sleep),
        )

        assert await proxy.run() == "ok"
        assert delays == [0.3, 2.0]

    def test_rejects_non_positive_max_attempts(self) -> None:
        """Test that constructing a RetryProxy with max_attempts < 1 fails loudly.

        **Why this test is important:**
          - max_attempts=0 would skip the loop and surface a confusing 'unreachable' RuntimeError at
            call time; rejecting it at construction turns a latent bug into a clear config error.

        **What it tests:**
          - RetryProxy(svc, max_attempts=0) raises ValueError.
        """
        with pytest.raises(ValueError, match="max_attempts"):
            RetryProxy(_AsyncPermanentBoom(), max_attempts=0)


class TestCircuitBreakerProxy:
    """Test suite for CircuitBreakerProxy async failure gating."""

    @pytest.mark.asyncio
    async def test_records_async_failures_and_opens(self) -> None:
        """Test that CircuitBreakerProxy records async failures and fails fast once open.

        **Why this test is important:**
          - A sync-only proxy never sees the async failure (it happens after the breaker context
            exits), so the breaker never opens; the async-first proxy must gate the awaited call.

        **What it tests:**
          - First call raises AppError and trips the breaker (threshold 1); the second call raises
            CircuitOpenError without invoking the service (call count stays at 1).
        """
        breaker = CircuitBreaker(failure_threshold=1)
        svc = _AsyncBoom()
        proxy = cast("_AsyncBoom", CircuitBreakerProxy(svc, breaker))

        with pytest.raises(AppError):
            await proxy.run()
        with pytest.raises(CircuitOpenError):
            await proxy.run()
        assert svc.calls == 1
