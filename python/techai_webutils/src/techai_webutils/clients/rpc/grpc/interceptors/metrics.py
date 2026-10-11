"""gRPC metrics interceptor."""

from __future__ import annotations

import time
from typing import TYPE_CHECKING

import grpc

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram


class MetricsInterceptor(grpc.aio.UnaryUnaryClientInterceptor):  # type: ignore[misc]
    """Async client interceptor that records gRPC call counts and latency."""

    def __init__(self, counter: MetricCounter, histogram: MetricHistogram) -> None:
        """Store the per-call ``counter`` (by method + status) and latency ``histogram`` (by method)."""
        self._counter = counter
        self._histogram = histogram

    async def intercept_unary_unary(  # type: ignore[override]
        self,
        continuation: Callable[..., Awaitable[grpc.aio.UnaryUnaryCall[object, object]]],
        client_call_details: grpc.aio.ClientCallDetails,
        request: object,
    ) -> object:
        """Intercept an async unary-unary call and record count and latency metrics."""
        method = client_call_details.method
        start = time.monotonic()
        try:
            call = await continuation(client_call_details, request)
            await call
            self._counter.increment(method=method, status="ok")
        except grpc.RpcError as e:
            self._counter.increment(method=method, status=str(e.code()))
            raise
        finally:
            self._histogram.observe(time.monotonic() - start, method=method)
        return call
