"""Tests for gRPC interceptor builder chain and per-interceptor call behavior (async client)."""

from __future__ import annotations

import asyncio
import json
from io import StringIO
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import grpc
import pytest

from techai_webutils.clients.rpc.grpc.interceptors.builder import InterceptorBuilder
from techai_webutils.clients.rpc.grpc.interceptors.logging import LoggingInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.metrics import MetricsInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.retry import RetryInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.timeout import TimeoutInterceptor
from techai_webutils.foundation.logger.logger import configure_logging

if TYPE_CHECKING:
    from collections.abc import Generator


def _rpc_error(code: grpc.StatusCode) -> grpc.RpcError:
    """Return a real grpc.RpcError (so ``except grpc.RpcError`` catches it) with a mocked ``code()``.

    The exception type must be real to be raisable/catchable; only its status code is mocked,
    which is what the transient-vs-fatal branch reads.
    """
    error = grpc.RpcError()
    error.code = MagicMock(return_value=code)
    return error


class _Call:
    """An awaitable stand-in for grpc.aio.UnaryUnaryCall: awaiting yields the response or raises.

    A fresh coroutine per ``__await__`` so the interceptor can await it (to surface errors) and the
    test can await the returned call again for the cached response — mirroring the real UnaryUnaryCall.
    """

    def __init__(
        self, response: object = None, error: grpc.RpcError | None = None
    ) -> None:
        """Configure the call to resolve to ``response`` or raise ``error`` when awaited."""
        self._response = response
        self._error = error

    def __await__(self) -> Generator[object, None, object]:
        """Yield the configured response, or raise the configured error."""

        async def _resolve() -> object:
            await asyncio.sleep(0)
            if self._error is not None:
                raise self._error
            return self._response

        return _resolve().__await__()


class TestInterceptorBuilder:
    """Test suite for InterceptorBuilder fluent chain assembly."""

    def test_build_empty(self) -> None:
        """Test that building without any interceptors returns an empty list.

        **Why this test is important:**
          - An empty interceptor chain is valid for lightweight internal services

        **What it tests:**
          - build() returns an empty list when no interceptors are configured
        """
        assert InterceptorBuilder().build() == []

    def test_chained_builder(self) -> None:
        """Test that fluent chaining produces interceptors in insertion order.

        **Why this test is important:**
          - Interceptor order affects behavior (logging wraps retry wraps timeout)

        **What it tests:**
          - build() returns Logging, Retry, Timeout in order
        """
        interceptors = (
            InterceptorBuilder().with_logging().with_retry().with_timeout().build()
        )
        assert len(interceptors) == 3
        assert isinstance(interceptors[0], LoggingInterceptor)
        assert isinstance(interceptors[1], RetryInterceptor)
        assert isinstance(interceptors[2], TimeoutInterceptor)


class TestRetryInterceptor:
    """Test suite for RetryInterceptor transient-failure retry behavior."""

    def test_rejects_non_positive_max_attempts(self) -> None:
        """Test that constructing a RetryInterceptor with max_attempts < 1 fails loudly.

        **Why this test is important:**
          - max_attempts=0 would skip the retry loop entirely and `raise None` (a confusing
            TypeError) on the first call; rejecting it at construction turns a latent bug into a
            clear config error.

        **What it tests:**
          - RetryInterceptor(max_attempts=0) raises ValueError.
        """
        with pytest.raises(ValueError, match="max_attempts"):
            RetryInterceptor(max_attempts=0)

    @pytest.mark.asyncio
    async def test_returns_result_without_retry_on_success(self) -> None:
        """Test that a call succeeding on the first attempt is not retried.

        **Why this test is important:**
          - The healthy path must add zero duplicate calls (non-idempotent requests).

        **What it tests:**
          - The continuation is awaited exactly once and the successful call is returned.
        """
        sentinel = object()
        continuation = AsyncMock(return_value=_Call(response=sentinel))
        interceptor = RetryInterceptor(max_attempts=3, base_delay=0.0)

        result = await interceptor.intercept_unary_unary(
            continuation, MagicMock(), MagicMock()
        )

        assert await cast("_Call", result) is sentinel
        continuation.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_retries_transient_error_then_succeeds(self) -> None:
        """Test that a transient error is retried and the subsequent success returned.

        **Why this test is important:**
          - Transient gRPC failures (UNAVAILABLE) are exactly what retry absorbs.

        **What it tests:**
          - A first UNAVAILABLE is swallowed, the call re-issued, and the success returned (2 attempts).
        """
        sentinel = object()
        continuation = AsyncMock(
            side_effect=[
                _Call(error=_rpc_error(grpc.StatusCode.UNAVAILABLE)),
                _Call(response=sentinel),
            ],
        )
        interceptor = RetryInterceptor(max_attempts=3, base_delay=0.0)

        result = await interceptor.intercept_unary_unary(
            continuation, MagicMock(), MagicMock()
        )

        assert await cast("_Call", result) is sentinel
        assert continuation.await_count == 2

    @pytest.mark.asyncio
    async def test_reraises_non_transient_error_without_retry(self) -> None:
        """Test that a non-transient error is raised immediately without retrying.

        **Why this test is important:**
          - Fatal errors (INVALID_ARGUMENT) never succeed on retry; retrying wastes time.

        **What it tests:**
          - An INVALID_ARGUMENT error propagates on the first attempt (continuation awaited once).
        """
        continuation = AsyncMock(
            return_value=_Call(error=_rpc_error(grpc.StatusCode.INVALID_ARGUMENT))
        )
        interceptor = RetryInterceptor(max_attempts=3, base_delay=0.0)

        with pytest.raises(grpc.RpcError):
            await interceptor.intercept_unary_unary(
                continuation, MagicMock(), MagicMock()
            )

        continuation.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_raises_last_error_after_exhausting_attempts(self) -> None:
        """Test that persistent transient failures exhaust the budget and raise.

        **Why this test is important:**
          - Retry must be bounded; after the budget is spent the error must surface.

        **What it tests:**
          - The continuation is awaited exactly max_attempts times, then the error is raised.
        """
        continuation = AsyncMock(
            return_value=_Call(error=_rpc_error(grpc.StatusCode.UNAVAILABLE))
        )
        interceptor = RetryInterceptor(max_attempts=3, base_delay=0.0)

        with pytest.raises(grpc.RpcError):
            await interceptor.intercept_unary_unary(
                continuation, MagicMock(), MagicMock()
            )

        assert continuation.await_count == 3


class TestTimeoutInterceptor:
    """Test suite for TimeoutInterceptor default-deadline application."""

    @staticmethod
    def _details(timeout: float | None) -> MagicMock:
        """Build minimal ClientCallDetails-shaped data for the interceptor."""
        return MagicMock(
            method="/svc/Method",
            timeout=timeout,
            metadata=None,
            credentials=None,
            wait_for_ready=None,
        )

    @pytest.mark.asyncio
    async def test_applies_default_timeout_when_unset(self) -> None:
        """Test that a default deadline is injected when the call has none.

        **Why this test is important:**
          - A call with no deadline can hang indefinitely and exhaust connections.

        **What it tests:**
          - The continuation receives call details whose timeout equals the configured default.
        """
        request = object()
        continuation = AsyncMock(return_value=_Call(response=object()))
        interceptor = TimeoutInterceptor(timeout_seconds=12.5)

        await interceptor.intercept_unary_unary(
            continuation, self._details(None), request
        )

        assert continuation.await_args is not None
        passed_details, passed_request = continuation.await_args.args
        assert passed_details.timeout == pytest.approx(12.5)
        assert passed_request is request

    @pytest.mark.asyncio
    async def test_preserves_caller_timeout_when_set(self) -> None:
        """Test that an explicit caller deadline is left untouched.

        **Why this test is important:**
          - A caller that set its own deadline knows its latency budget best.

        **What it tests:**
          - The original call details object is forwarded unchanged (timeout preserved).
        """
        details = self._details(3.0)
        continuation = AsyncMock(return_value=_Call(response=object()))
        interceptor = TimeoutInterceptor(timeout_seconds=12.5)

        await interceptor.intercept_unary_unary(continuation, details, object())

        assert continuation.await_args is not None
        passed_details = continuation.await_args.args[0]
        assert passed_details is details
        assert passed_details.timeout == pytest.approx(3.0)


class TestLoggingInterceptor:
    """Test suite for LoggingInterceptor call observability."""

    @pytest.mark.asyncio
    async def test_logs_completion_as_structured_json_with_duration(self) -> None:
        """A successful call is logged as STRUCTURED JSON (method + duration_ms), and returned.

        **Why this test is important:**
          - The gRPC client interceptor is the Python client-seam analog of the Go decorators:
            its per-call debug log must be JSON on the canonical schema (trace-correlatable,
            timed), not a printf ``%s`` stdlib line — otherwise the outbound-RPC stage is
            invisible to JSON log queries and un-timeable.

        **What it tests:**
          - The call is returned unchanged and a structured 'grpc client call complete' line
            carrying ``method`` and a numeric ``duration_ms`` at level ``debug`` is emitted.
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)

        sentinel = object()
        continuation = AsyncMock(return_value=_Call(response=sentinel))
        interceptor = LoggingInterceptor(logger_name="test.grpc.client")
        details = MagicMock(method="/svc/Method")

        result = await interceptor.intercept_unary_unary(continuation, details, object())
        assert await cast("_Call", result) is sentinel

        records = [
            json.loads(line) for line in output.getvalue().strip().split("\n") if line
        ]
        complete = [r for r in records if r.get("message") == "grpc client call complete"]
        assert complete, records
        assert complete[-1]["method"] == "/svc/Method"
        assert isinstance(complete[-1]["duration_ms"], (int, float))
        assert complete[-1]["level"] == "debug"

    @pytest.mark.asyncio
    async def test_logs_and_reraises_on_error(self) -> None:
        """A failing call is logged as structured JSON and re-raised.

        **Why this test is important:**
          - A swallowed gRPC error would hide failures from the caller; the failure line must be
            structured (the inner-seam DEBUG mirror of the Go decorators), not a printf stdlib line.

        **What it tests:**
          - The gRPC error propagates and a structured 'grpc client call failed' line (carrying
            ``method``) is emitted.
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)

        continuation = AsyncMock(
            return_value=_Call(error=_rpc_error(grpc.StatusCode.UNAVAILABLE))
        )
        interceptor = LoggingInterceptor(logger_name="test.grpc.client")
        details = MagicMock(method="/svc/Method")

        with pytest.raises(grpc.RpcError):
            await interceptor.intercept_unary_unary(continuation, details, object())

        records = [
            json.loads(line) for line in output.getvalue().strip().split("\n") if line
        ]
        failed = [r for r in records if r.get("message") == "grpc client call failed"]
        assert failed, records
        assert failed[-1]["method"] == "/svc/Method"


class TestMetricsInterceptor:
    """Test suite for MetricsInterceptor counter/latency recording."""

    @pytest.mark.asyncio
    async def test_records_success_count_and_latency(self) -> None:
        """Test that a successful call increments the ok counter and observes latency.

        **Why this test is important:**
          - Success-rate and latency metrics drive dashboards and SLO alerting.

        **What it tests:**
          - The counter is incremented status 'ok' and the latency histogram observed once.
        """
        counter, histogram = MagicMock(), MagicMock()
        sentinel = object()
        continuation = AsyncMock(return_value=_Call(response=sentinel))
        interceptor = MetricsInterceptor(counter, histogram)
        details = MagicMock(method="/svc/Method")

        result = await interceptor.intercept_unary_unary(continuation, details, object())

        assert await cast("_Call", result) is sentinel
        counter.increment.assert_called_once_with(method="/svc/Method", status="ok")
        histogram.observe.assert_called_once()

    @pytest.mark.asyncio
    async def test_records_error_status_and_reraises(self) -> None:
        """Test that a failing call records the error status code and re-raises.

        **Why this test is important:**
          - Error metrics labelled by status code distinguish failure modes; latency still recorded.

        **What it tests:**
          - The counter is incremented with the failure status, latency observed, and the error raised.
        """
        counter, histogram = MagicMock(), MagicMock()
        continuation = AsyncMock(
            return_value=_Call(error=_rpc_error(grpc.StatusCode.UNAVAILABLE))
        )
        interceptor = MetricsInterceptor(counter, histogram)
        details = MagicMock(method="/svc/Method")

        with pytest.raises(grpc.RpcError):
            await interceptor.intercept_unary_unary(continuation, details, object())

        assert counter.increment.call_args.kwargs["method"] == "/svc/Method"
        assert counter.increment.call_args.kwargs["status"] == str(
            grpc.StatusCode.UNAVAILABLE
        )
        histogram.observe.assert_called_once()
