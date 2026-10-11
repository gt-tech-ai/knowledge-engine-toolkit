"""HTTP server observability middleware (metrics + tracing).

Provides framework adapters (aiohttp, FastAPI/Starlette) that emit the
RED-style metrics the Grafana dashboards query and start a SERVER span per
request, propagating W3C trace context from inbound headers so traces span the
whole app:

    http_server_requests_total{method,route,status}
    http_server_request_duration_seconds{method,route,status}

Metric objects are module-level singletons so importing this module twice (or
mounting both adapters) never double-registers with the Prometheus registry.
"""

from __future__ import annotations

import time
from typing import TYPE_CHECKING, Any

from opentelemetry import trace as otel_trace
from opentelemetry.propagate import extract
from opentelemetry.trace import SpanKind, Status, StatusCode
from prometheus_client import Counter, Histogram

from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from starlette.requests import Request
    from starlette.responses import Response

_log = get_logger("http")

_DURATION_BUCKETS = (0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10)
"""Latency histogram bucket upper bounds, in seconds (5ms to 10s)."""

_REQUESTS = Counter(
    "http_server_requests_total",
    "Total number of HTTP server requests",
    ["method", "route", "status"],
)
"""RED request counter labelled by method/route/status (module singleton)."""
_DURATION = Histogram(
    "http_server_request_duration_seconds",
    "HTTP server request duration in seconds",
    ["method", "route", "status"],
    buckets=_DURATION_BUCKETS,
)
"""RED request-duration histogram labelled by method/route/status (module singleton)."""


def _record(method: str, route: str, status: int, started: float) -> None:
    """Increment the request counter and observe latency for one completed request.

    ``started`` is a ``time.perf_counter()`` reading taken when the request began;
    the elapsed duration is recorded against the labelled histogram.
    """
    labels = {"method": method, "route": route, "status": str(status)}
    _REQUESTS.labels(**labels).inc()
    _DURATION.labels(**labels).observe(time.perf_counter() - started)


_CLIENT_ERROR_STATUS = 400
"""Lowest HTTP status logged at WARNING (4xx client-error threshold)."""
_SERVER_ERROR_STATUS = 500
"""Lowest HTTP status logged at ERROR and marking the span failed (5xx threshold)."""

# Infra paths whose access logs are suppressed: kubelet probes /healthz + /readyz
# and Prometheus scrapes /metrics, all every ~15s — access-logging them buries the
# real request logs (the /metrics scrape alone floods a service's log). RED metrics
# are still recorded (via _record), so probe/scrape latency and failures remain
# visible on the dashboards; only the redundant access-log line is dropped. Trade-off:
# a FAILED scrape too (a 5xx on /metrics) loses its access-log line — it stays visible
# only as a RED metric on the dashboard, not in the logs.
_SUPPRESSED_ACCESS_LOG_PATHS = frozenset({"/healthz", "/readyz", "/metrics"})
"""Probe/scrape paths whose access-log line is dropped (RED metrics still recorded)."""


def _log_http(
    method: str,
    path: str,
    status: int,
    started: float,
    *,
    route: str | None = None,
    exc_info: bool = False,
) -> None:
    """Emit the canonical 'http request' log at a status-appropriate level.

    5xx -> error (with a structured traceback when exc_info is set), 4xx ->
    warning, else info. Health-probe (/healthz, /readyz) and Prometheus-scrape
    (/metrics) paths are skipped so kubelet + Prometheus traffic does not bury
    real request logs (RED metrics are still recorded by the caller).
    """
    if path in _SUPPRESSED_ACCESS_LOG_PATHS:
        return
    fields: dict[str, object] = {
        "method": method,
        "path": path,
        "status": status,
        "duration_ms": round((time.perf_counter() - started) * 1000, 2),
    }
    if route is not None:
        fields["route"] = route
    if status >= _SERVER_ERROR_STATUS:
        if exc_info:
            _log.error("http request", exc_info=True, **fields)
        else:
            _log.error("http request", **fields)
    elif status >= _CLIENT_ERROR_STATUS:
        _log.warning("http request", **fields)
    else:
        _log.info("http request", **fields)


def aiohttp_observability_middleware(service_name: str) -> Any:  # noqa: ANN401  (aiohttp middleware type)
    """Return an aiohttp @middleware that records metrics + a SERVER span.

    aiohttp is imported lazily so techai_webutils does not hard-depend on it.
    """
    from aiohttp import web  # noqa: PLC0415  (lazy: aiohttp is a caller dependency)

    tracer = otel_trace.get_tracer(service_name)

    @web.middleware
    async def middleware(
        request: web.Request,
        handler: Callable[[web.Request], Awaitable[web.StreamResponse]],
    ) -> web.StreamResponse:
        """Wrap one aiohttp request in a SERVER span and emit metrics + a log.

        Resolves the route template for low-cardinality metric labels, records
        the outcome for success, ``HTTPException``, and unexpected crashes, and
        re-raises so the framework's normal error handling still runs.
        """
        route = request.path
        if request.match_info.route.resource is not None:
            route = request.match_info.route.resource.canonical
        started = time.perf_counter()
        parent = extract(dict(request.headers))

        with tracer.start_as_current_span(
            f"{request.method} {route}", context=parent, kind=SpanKind.SERVER
        ) as span:
            span.set_attribute("http.request.method", request.method)
            span.set_attribute("url.path", request.path)
            try:
                resp = await handler(request)
            except web.HTTPException as exc:
                span.set_attribute("http.response.status_code", exc.status)
                if exc.status >= _SERVER_ERROR_STATUS:
                    span.set_status(Status(StatusCode.ERROR))
                _record(request.method, route, exc.status, started)
                _log_http(request.method, request.path, exc.status, started, route=route)
                raise
            except Exception as exc:
                span.record_exception(exc)
                span.set_status(Status(StatusCode.ERROR))
                _record(request.method, route, _SERVER_ERROR_STATUS, started)
                _log_http(
                    request.method,
                    request.path,
                    _SERVER_ERROR_STATUS,
                    started,
                    route=route,
                    exc_info=True,
                )
                raise
            span.set_attribute("http.response.status_code", resp.status)
            _record(request.method, route, resp.status, started)
            _log_http(request.method, request.path, resp.status, started, route=route)
            return resp

    return middleware


async def fastapi_observability_middleware(
    request: Request,
    call_next: Callable[[Request], Awaitable[Response]],
) -> Response:
    """FastAPI/Starlette HTTP middleware: register via app.middleware('http').

    Records metrics + a SERVER span, propagating inbound W3C trace context.
    """
    tracer = otel_trace.get_tracer(request.app.title or "fastapi")
    route = request.url.path
    started = time.perf_counter()
    parent = extract(dict(request.headers))

    with tracer.start_as_current_span(
        f"{request.method} {route}", context=parent, kind=SpanKind.SERVER
    ) as span:
        span.set_attribute("http.request.method", request.method)
        span.set_attribute("url.path", route)
        try:
            response = await call_next(request)
        except Exception as exc:
            span.record_exception(exc)
            span.set_status(Status(StatusCode.ERROR))
            _record(request.method, route, _SERVER_ERROR_STATUS, started)
            _log_http(request.method, route, _SERVER_ERROR_STATUS, started, exc_info=True)
            raise
        span.set_attribute("http.response.status_code", response.status_code)
        if response.status_code >= _SERVER_ERROR_STATUS:
            span.set_status(Status(StatusCode.ERROR))
        _record(request.method, route, response.status_code, started)
        _log_http(request.method, route, response.status_code, started)
        return response
