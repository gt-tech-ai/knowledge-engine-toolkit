"""gRPC client interceptor for distributed tracing.

Creates an OTel span per outbound RPC, matching Go's tracing interceptor
in ``go/clients/transport/connect/interceptors/tracing.go``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import grpc

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.tracer import TracerProvider


class TracingInterceptor(grpc.aio.UnaryUnaryClientInterceptor):  # type: ignore[misc]
    """Async client interceptor that creates a span per outbound RPC call."""

    def __init__(self, tracer: TracerProvider) -> None:
        """Store the ``tracer`` provider that yields the client-kind span opened per outbound RPC."""
        self._tracer = tracer

    async def intercept_unary_unary(  # type: ignore[override]
        self,
        continuation: Callable[..., Awaitable[grpc.aio.UnaryUnaryCall[object, object]]],
        client_call_details: grpc.aio.ClientCallDetails,
        request: object,
    ) -> object:
        """Intercept the async RPC call and wrap it in a tracing span."""
        method = client_call_details.method or "unknown"
        with self._tracer.span(f"grpc.client/{method}", kind="client") as span:
            try:
                call = await continuation(client_call_details, request)
                await call
                return call
            except Exception as exc:
                span.record_error(exc)
                raise
