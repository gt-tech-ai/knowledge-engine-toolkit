"""gRPC circuit breaker interceptor for fault isolation.

Wraps gRPC calls in a circuit breaker to prevent cascading failures
when downstream services are unhealthy. When the circuit is open,
calls fail fast with UNAVAILABLE status.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

import grpc

from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitBreaker,
    CircuitOpenError,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


class _CircuitOpenRpcError(grpc.RpcError):
    """An ``RpcError`` carrying ``UNAVAILABLE`` so callers (and the retry interceptor) can classify it.

    A bare ``grpc.RpcError()`` has no ``code()``/``details()``, which both misleads callers and breaks
    the retry interceptor's ``e.code()`` transient check — hence this minimal typed error.
    """

    @override
    def code(self) -> grpc.StatusCode:
        """Return ``UNAVAILABLE`` — the circuit is open, so the call was not attempted."""
        return grpc.StatusCode.UNAVAILABLE

    @override
    def details(self) -> str:
        """Return a human-readable reason."""
        return "circuit breaker open"


class CircuitBreakerInterceptor(grpc.aio.UnaryUnaryClientInterceptor):  # type: ignore[misc]
    """Async client interceptor that wraps gRPC calls in a circuit breaker.

    When the circuit is open, calls fail immediately with an ``UNAVAILABLE`` ``grpc.RpcError`` instead
    of attempting the downstream call.
    """

    def __init__(self, cb: CircuitBreaker) -> None:
        """Store the circuit breaker whose state gates each intercepted call."""
        self._cb = cb

    async def intercept_unary_unary(  # type: ignore[override]
        self,
        continuation: Callable[..., Awaitable[grpc.aio.UnaryUnaryCall[object, object]]],
        client_call_details: grpc.aio.ClientCallDetails,
        request: object,
    ) -> object:
        """Intercept an async unary-unary call and apply circuit breaker protection.

        Awaits the call inside the breaker context so a downstream failure trips it; returns the
        successful call for the caller to await (cached response). When the breaker is open, raises an
        ``UNAVAILABLE`` ``RpcError`` without attempting the downstream call.
        """
        try:
            with self._cb:
                call = await continuation(client_call_details, request)
                await call
                return call
        except CircuitOpenError as e:
            raise _CircuitOpenRpcError from e
