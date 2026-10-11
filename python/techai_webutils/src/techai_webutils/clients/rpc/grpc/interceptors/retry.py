"""gRPC retry interceptor for transient errors."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

import grpc

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

_TRANSIENT_CODES = frozenset({
    grpc.StatusCode.UNAVAILABLE,
    grpc.StatusCode.DEADLINE_EXCEEDED,
    grpc.StatusCode.ABORTED,
})
"""gRPC status codes treated as transient and eligible for retry with backoff."""


class RetryInterceptor(grpc.aio.UnaryUnaryClientInterceptor):  # type: ignore[misc]
    """Async client interceptor that retries transient errors with exponential backoff."""

    def __init__(self, max_attempts: int = 3, base_delay: float = 0.1) -> None:
        """Configure the retry budget (minimum 1 attempt) and the exponential-backoff base delay.

        ``max_attempts`` is the total number of tries before the last transient error is re-raised;
        ``base_delay`` (seconds) is doubled per attempt. A ``max_attempts < 1`` is rejected loudly
        rather than silently skipping the loop and raising ``None``.
        """
        if max_attempts < 1:
            msg = f"max_attempts must be >= 1, got {max_attempts}"
            raise AppValueError(msg)
        self._max_attempts = max_attempts
        self._base_delay = base_delay

    async def intercept_unary_unary(  # type: ignore[override]
        self,
        continuation: Callable[..., Awaitable[grpc.aio.UnaryUnaryCall[object, object]]],
        client_call_details: grpc.aio.ClientCallDetails,
        request: object,
    ) -> object:
        """Intercept an async unary-unary call, retrying transient errors with backoff.

        Awaits each attempt's call to surface (and classify) errors, then returns the successful
        call — awaiting it again yields the cached response for the caller.
        """
        last_error: grpc.RpcError | None = None
        for attempt in range(self._max_attempts):
            call = await continuation(client_call_details, request)
            try:
                await call
            except grpc.RpcError as e:
                if e.code() not in _TRANSIENT_CODES:
                    raise
                last_error = e
                if attempt < self._max_attempts - 1:
                    await asyncio.sleep(self._base_delay * (2**attempt))
                continue
            return call
        raise last_error  # type: ignore[misc]
