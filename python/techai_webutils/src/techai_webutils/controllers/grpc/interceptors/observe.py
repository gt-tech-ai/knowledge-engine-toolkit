"""The gRPC-native interceptors standing in.

For the controller tier's observability layers.

They mirror Go ``transport/grpc/interceptors``. Each runs around the whole RPC — a stream
included — and reads the RPC's final status, so a span, a request count and an access line
carry the status the caller got.
"""

from __future__ import annotations

import time
from typing import TYPE_CHECKING, cast, final, override

import grpc
from opentelemetry import context as otel_context
from opentelemetry import trace as otel_trace
from opentelemetry.propagate import extract
from opentelemetry.trace import SpanKind, Status, StatusCode
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

from techai_webutils.controllers.grpc.interceptors.behavior import rebuild

if TYPE_CHECKING:
    from techai_webutils.controllers.grpc.interceptors.behavior import (
        Behavior,
        Context,
        Continuation,
    )
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricsProvider

_RESPONSE_PROPAGATOR = TraceContextTextMapPropagator()
"""W3C TraceContext propagator used directly, so the ``traceresponse`` value is
deterministic."""

_DURATION_BUCKETS = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
"""The request-duration histogram buckets, in seconds (Prometheus' defaults)."""


def parse_method(method: str) -> tuple[str, str]:
    """Split ``/pkg.Service/Method`` into its service and method.

    Returns:
        The service and the method.

    """
    service, _, name = method.lstrip("/").partition("/")
    return (service, name) if name else ("", method)


def status_name(context: Context, err: BaseException | None) -> str:
    """Return the RPC's final status as Go names it (``OK``, ``ResourceExhausted``, …).

    Returns:
        The RPC's final status as Go names it (``OK``, ``ResourceExhausted``, …).

    """
    if err is None:
        return "OK"
    # The status an abort set; an uncaught exception set none, and gRPC returns UNKNOWN.
    code = cast("grpc.StatusCode | None", context.code()) or grpc.StatusCode.UNKNOWN
    if code is grpc.StatusCode.OK:
        return "OK"
    if code is grpc.StatusCode.CANCELLED:
        return "Canceled"
    return "".join(word.capitalize() for word in code.name.split("_"))


def _log_rpc_failure(
    logger: Logger, method: str, start: float, context: Context, exc: Exception
) -> None:
    """Log a failed RPC with its status; a failure is not a crash, so no traceback."""
    logger.error(
        "gRPC request failed",
        method=method,
        duration=time.perf_counter() - start,
        code=status_name(context, exc),
        error=str(exc),
    )


@final
class TracingServerInterceptor(grpc.aio.ServerInterceptor):
    """Continues the caller's trace in one SERVER span per RPC.

    Returned as ``traceresponse``.

    The span is parented on the W3C trace context in the incoming metadata, named by the
    full method and carries ``rpc.system``, ``rpc.service`` and ``rpc.method``; it is
    current for the whole call, so every inner span shares the upstream trace. The inbound
    context is attached for the call too, so the handler reads the caller's W3C baggage.
    """

    def __init__(self, scope: str) -> None:
        """Acquire the OTel tracer whose instrumentation scope is ``scope``."""
        self._tracer = otel_trace.get_tracer(scope)

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Continuation[TRequest, TResponse],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Wrap the handler in a SERVER span parented on the inbound context.

        Returns:
            The handler to serve the call, or ``None`` when the method is unknown.

        """
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler
        method = handler_call_details.method or "unknown"
        parent = extract(dict(handler_call_details.invocation_metadata or []))
        service, name = parse_method(method)
        tracer = self._tracer

        def wrap(behavior: Behavior, *, streaming: bool) -> Behavior:
            del streaming

            async def traced(request: object, context: Context) -> object:
                # Attach the inbound context (baggage included) for the whole call; the
                # span below parents on it. Detached in finally, so an error leaves no
                # residue.
                token = otel_context.attach(parent)
                try:
                    # A cancelled stream is not an error: only an Exception marks the
                    # span.
                    with tracer.start_as_current_span(
                        method,
                        context=parent,
                        kind=SpanKind.SERVER,
                        record_exception=False,
                        set_status_on_exception=False,
                    ) as span:
                        span.set_attribute("rpc.system", "grpc")
                        span.set_attribute("rpc.service", service)
                        span.set_attribute("rpc.method", name)
                        _set_trace_response(context)
                        try:
                            return await behavior(request, context)
                        except Exception as exc:
                            span.record_exception(exc)
                            span.set_status(Status(StatusCode.ERROR))
                            raise
                finally:
                    otel_context.detach(token)

            return traced

        return rebuild(handler, wrap)


def _set_trace_response(context: Context) -> None:
    """Return the active span's trace context to the caller.

    As ``traceresponse`` trailing metadata.
    """
    carrier: dict[str, str] = {}
    _RESPONSE_PROPAGATOR.inject(carrier)
    traceparent = carrier.get("traceparent")
    if traceparent:
        context.set_trailing_metadata((("traceresponse", traceparent),))


@final
class MetricsServerInterceptor(grpc.aio.ServerInterceptor):
    """Counts requests by final status and observes their duration.

    ``grpc_server_requests_total{service,method,code}`` and
    ``grpc_server_duration_seconds{service,method}`` — the series the Go gRPC server
    records.
    """

    def __init__(self, metrics: MetricsProvider) -> None:
        """Create (or retrieve) the instruments."""
        self._requests = metrics.counter(
            "grpc_server_requests_total",
            "Total number of gRPC server requests",
            ["service", "method", "code"],
        )
        self._duration = metrics.histogram(
            "grpc_server_duration_seconds",
            "gRPC server request duration in seconds",
            ["service", "method"],
            _DURATION_BUCKETS,
        )

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Continuation[TRequest, TResponse],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Wrap the handler to count and time it.

        Returns:
            The handler to serve the call, or ``None`` when the method is unknown.

        """
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler
        service, name = parse_method(handler_call_details.method or "unknown")
        requests, duration = self._requests, self._duration

        def wrap(behavior: Behavior, *, streaming: bool) -> Behavior:
            del streaming

            async def measured(request: object, context: Context) -> object:
                start = time.perf_counter()
                err: BaseException | None = None
                try:
                    return await behavior(request, context)
                except Exception as exc:
                    err = exc
                    raise
                finally:
                    duration.observe(
                        time.perf_counter() - start, service=service, method=name
                    )
                    code = status_name(context, err)
                    requests.inc(1.0, service=service, method=name, code=code)

            return measured

        return rebuild(handler, wrap)


@final
class LoggingServerInterceptor(grpc.aio.ServerInterceptor):
    """Writes one access line per RPC: ``gRPC request completed`` (info) or ``… failed``.

    (error).
    """

    def __init__(self, logger: Logger) -> None:
        """Bind the logger."""
        self._logger = logger

    @override
    async def intercept_service[TRequest, TResponse](
        self,
        continuation: Continuation[TRequest, TResponse],
        handler_call_details: grpc.HandlerCallDetails,
    ) -> grpc.RpcMethodHandler[TRequest, TResponse] | None:
        """Wrap the handler to log its outcome.

        Returns:
            The handler to serve the call, or ``None`` when the method is unknown.

        """
        handler = await continuation(handler_call_details)
        if handler is None:
            return handler
        method = handler_call_details.method or "unknown"
        logger = self._logger

        def wrap(behavior: Behavior, *, streaming: bool) -> Behavior:
            del streaming

            async def logged(request: object, context: Context) -> object:
                start = time.perf_counter()
                try:
                    result = await behavior(request, context)
                except Exception as exc:
                    _log_rpc_failure(logger, method, start, context, exc)
                    raise
                logger.info(
                    "gRPC request completed",
                    method=method,
                    duration=time.perf_counter() - start,
                    code="OK",
                )
                return result

            return logged

        return rebuild(handler, wrap)
