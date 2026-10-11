"""Fluent builder for composing gRPC client interceptors.

Order matches Go: timeout → retry → circuit_breaker → metrics → tracing → logging.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.clients.rpc.grpc.interceptors.circuit_breaker import (
    CircuitBreakerInterceptor,
)
from techai_webutils.clients.rpc.grpc.interceptors.logging import LoggingInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.metrics import MetricsInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.retry import RetryInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.timeout import TimeoutInterceptor
from techai_webutils.clients.rpc.grpc.interceptors.tracing import TracingInterceptor

if TYPE_CHECKING:
    import grpc

    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.tracer import TracerProvider
    from techai_webutils.foundation.resilience.circuit_breaker import CircuitBreaker


class InterceptorBuilder:
    """Compose gRPC client interceptors in a fluent chain.

    Example::

        interceptors = (InterceptorBuilder()
            .with_logging("my-service")
            .with_tracing(tracer)
            .with_retry(max_attempts=3)
            .with_timeout(10.0)
            .build())
        channel = grpc.intercept_channel(channel, *interceptors)
    """

    def __init__(self) -> None:
        """Start with an empty, append-ordered list of async client interceptors."""
        self._interceptors: list[grpc.aio.UnaryUnaryClientInterceptor] = []  # type: ignore[type-arg]

    def with_logging(self, logger_name: str = "grpc.client") -> InterceptorBuilder:
        """Add logging interceptor."""
        self._interceptors.append(LoggingInterceptor(logger_name))
        return self

    def with_retry(
        self, max_attempts: int = 3, base_delay: float = 0.1
    ) -> InterceptorBuilder:
        """Add retry interceptor."""
        self._interceptors.append(RetryInterceptor(max_attempts, base_delay))
        return self

    def with_timeout(self, timeout_seconds: float = 30.0) -> InterceptorBuilder:
        """Add timeout interceptor."""
        self._interceptors.append(TimeoutInterceptor(timeout_seconds))
        return self

    def with_circuit_breaker(self, cb: CircuitBreaker) -> InterceptorBuilder:
        """Add circuit breaker interceptor."""
        self._interceptors.append(CircuitBreakerInterceptor(cb))
        return self

    def with_metrics(
        self, counter: MetricCounter, histogram: MetricHistogram
    ) -> InterceptorBuilder:
        """Add metrics interceptor."""
        self._interceptors.append(MetricsInterceptor(counter, histogram))
        return self

    def with_tracing(self, tracer: TracerProvider) -> InterceptorBuilder:
        """Add tracing interceptor."""
        self._interceptors.append(TracingInterceptor(tracer))
        return self

    def build(self) -> list[grpc.aio.UnaryUnaryClientInterceptor]:  # type: ignore[type-arg]
        """Return the composed list of async client interceptors."""
        return list(self._interceptors)
