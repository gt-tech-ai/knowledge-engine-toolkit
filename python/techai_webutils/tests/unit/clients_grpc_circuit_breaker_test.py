"""Tests for gRPC circuit breaker interceptor and builder integration."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import grpc
import pytest

from techai_webutils.clients.rpc.grpc.interceptors.builder import InterceptorBuilder
from techai_webutils.clients.rpc.grpc.interceptors.circuit_breaker import (
    CircuitBreakerInterceptor,
)
from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitBreaker,
    CircuitOpenError,
)

if TYPE_CHECKING:
    from collections.abc import Generator


class _Call:
    """An awaitable stand-in for grpc.aio.UnaryUnaryCall (awaiting yields the response)."""

    def __init__(self, response: object) -> None:
        """Configure the response the call resolves to when awaited."""
        self._response = response

    def __await__(self) -> Generator[object, None, object]:
        """Resolve to the configured response."""

        async def _resolve() -> object:
            await asyncio.sleep(0)
            return self._response

        return _resolve().__await__()


class TestCircuitBreakerInterceptor:
    """Test suite for CircuitBreakerInterceptor gRPC call gating."""

    @pytest.mark.asyncio
    async def test_passes_when_closed(self) -> None:
        """Test that gRPC calls proceed normally when the circuit breaker is closed.

        **Why this test is important:**
          - The circuit breaker must not block calls when the downstream service is healthy.

        **What it tests:**
          - Continuation is awaited once and the successful call is returned unchanged.
        """
        # A closed breaker: entering its `with` context is a no-op that lets the call through.
        cb = MagicMock(spec=CircuitBreaker)
        interceptor = CircuitBreakerInterceptor(cb)

        sentinel = object()
        continuation = AsyncMock(return_value=_Call(sentinel))
        call_details = MagicMock()
        request = MagicMock()

        result = await interceptor.intercept_unary_unary(
            continuation, call_details, request
        )
        assert await cast("_Call", result) is sentinel
        continuation.assert_awaited_once_with(call_details, request)

    @pytest.mark.asyncio
    async def test_raises_when_open(self) -> None:
        """Test that gRPC calls are rejected with RpcError when the circuit is open.

        **Why this test is important:**
          - Open circuit must prevent requests reaching a failing downstream (no cascading failure).

        **What it tests:**
          - An RpcError carrying UNAVAILABLE is raised and the continuation is never awaited.
        """
        # An open breaker: entering its `with` context raises CircuitOpenError before the call runs.
        cb = MagicMock(spec=CircuitBreaker)
        cb.__enter__.side_effect = CircuitOpenError()
        interceptor = CircuitBreakerInterceptor(cb)

        continuation = AsyncMock()
        call_details = MagicMock()
        request = MagicMock()

        with pytest.raises(grpc.RpcError) as excinfo:
            await interceptor.intercept_unary_unary(continuation, call_details, request)

        # The open-circuit error must carry a real status code (UNAVAILABLE), not a bare RpcError —
        # otherwise callers (and the retry interceptor's e.code() check) can't classify it.
        assert excinfo.value.code() == grpc.StatusCode.UNAVAILABLE
        continuation.assert_not_awaited()

    def test_builder_includes_circuit_breaker(self) -> None:
        """Test that InterceptorBuilder includes CircuitBreakerInterceptor when configured.

        **Why this test is important:**
          - The builder pattern is how production code assembles interceptor chains
          - The circuit breaker must be correctly wired through the builder API
          - Missing integration would silently disable circuit breaking in production

        **What it tests:**
          - Builder produces exactly 1 interceptor
          - The interceptor is an instance of CircuitBreakerInterceptor
        """
        cb = CircuitBreaker(failure_threshold=5, recovery_timeout=30.0)
        interceptors = InterceptorBuilder().with_circuit_breaker(cb).build()

        assert len(interceptors) == 1
        assert isinstance(interceptors[0], CircuitBreakerInterceptor)

    def test_builder_circuit_breaker_with_others(self) -> None:
        """Test that the circuit breaker composes correctly with logging and timeout interceptors.

        **Why this test is important:**
          - Production gRPC channels use multiple interceptors stacked together
          - The circuit breaker must not interfere with logging or timeout interceptors
          - Interceptor ordering affects behavior (e.g., logging should capture circuit open events)

        **What it tests:**
          - Builder produces exactly 3 interceptors (logging, circuit breaker, timeout)
          - The second interceptor is a CircuitBreakerInterceptor
        """
        cb = CircuitBreaker(failure_threshold=5, recovery_timeout=30.0)
        interceptors = (
            InterceptorBuilder()
            .with_logging("test")
            .with_circuit_breaker(cb)
            .with_timeout(10.0)
            .build()
        )

        assert len(interceptors) == 3
        assert isinstance(interceptors[1], CircuitBreakerInterceptor)
