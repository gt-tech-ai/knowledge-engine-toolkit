"""gRPC logging interceptor."""

from __future__ import annotations

import time
from typing import TYPE_CHECKING

import grpc

from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


class LoggingInterceptor(grpc.aio.UnaryUnaryClientInterceptor):  # type: ignore[misc]
    """Async client interceptor that logs each outbound gRPC call as a STRUCTURED event.

    The Python client-seam analog of the Go clients/decorators logging decorator: entry
    (``grpc client call``), completion (``grpc client call complete``, with ``duration_ms``), and
    failure (``grpc client call failed``) are logged at DEBUG through the shared structlog logger,
    so every line is JSON on the canonical schema and carries the active span's trace ids (injected
    by the logger's ``_add_trace_context`` processor). Inner seam: DEBUG only (visible in dev;
    suppressed in staging/prod, where only the outermost seam logs — ARCHITECTURE.md#decorator-order).
    """

    def __init__(self, logger_name: str = "grpc.client") -> None:
        """Resolve the structlog logger (named ``logger_name``) used for per-call structured logging."""
        self._logger = get_logger(logger_name)

    async def intercept_unary_unary(  # type: ignore[override]
        self,
        continuation: Callable[..., Awaitable[grpc.aio.UnaryUnaryCall[object, object]]],
        client_call_details: grpc.aio.ClientCallDetails,
        request: object,
    ) -> object:
        """Intercept an async unary-unary call and log entry, completion (timed), and errors (at debug)."""
        method = client_call_details.method
        self._logger.debug("grpc client call", method=method)
        start = time.perf_counter()
        try:
            call = await continuation(client_call_details, request)
            await call
        except grpc.RpcError:
            self._logger.debug(
                "grpc client call failed",
                method=method,
                duration_ms=round((time.perf_counter() - start) * 1000, 2),
                exc_info=True,
            )
            raise
        self._logger.debug(
            "grpc client call complete",
            method=method,
            duration_ms=round((time.perf_counter() - start) * 1000, 2),
        )
        return call
