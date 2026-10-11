"""Tests for the observability helpers.

They cover trace-context logging, HTTP metrics, the gRPC tracing server interceptor, and the
setup bootstrap.
"""

from __future__ import annotations

import asyncio
import json
from io import StringIO
from unittest.mock import AsyncMock

import grpc
import pytest

from techai_webutils.clients.rpc.grpc.interceptors.tracing_server import (
    TracingServerInterceptor,
)
from techai_webutils.foundation.logger.logger import configure_logging
from techai_webutils.foundation.middleware import http_observability
from techai_webutils.foundation.observability import parse_sample_rate


def _serve(path: str, status: int = 200) -> None:
    """Run one GET ``path`` returning ``status`` through the FastAPI observability middleware."""
    from starlette.requests import Request
    from starlette.responses import PlainTextResponse, Response

    class _App:
        title = "test-svc"

    scope = {
        "type": "http",
        "method": "GET",
        "path": path,
        "headers": [],
        "query_string": b"",
        "app": _App(),
    }

    async def call_next(_request: Request) -> Response:
        await asyncio.sleep(0)
        return PlainTextResponse("ok", status_code=status)

    asyncio.run(
        http_observability.fastapi_observability_middleware(Request(scope), call_next)
    )


def _http_logs(raw: str) -> list[dict[str, object]]:
    """Return the rendered 'http request' log entries in ``raw``."""
    entries = [json.loads(line) for line in raw.strip().split("\n") if line]
    return [e for e in entries if e.get("message") == "http request"]


class TestTraceContextProcessor:
    """Tests for the trace context processor."""

    def test_no_span_is_noop(self) -> None:
        """Test that the trace-context processor adds nothing when no span is active.

        **Why this test is important:**
          - The processor runs on every log line, including code paths with no active
            OTel span (startup, background tasks). It must stay a clean no-op there;
            emitting an all-zero or garbage trace_id would pollute logs and create
            dead log-to-trace links that resolve to no trace

        **What it tests:**
          - With no recording span, a rendered log line carries no trace_id/span_id
        """
        import structlog

        output = StringIO()
        configure_logging(level="INFO", stream=output)
        structlog.get_logger("t").info("hi")
        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert "trace_id" not in data
        assert "span_id" not in data

    def test_logger_emits_canonical_message_key(self) -> None:
        """Test that the configured logger renders the log message under the 'message' key.

        **Why this test is important:**
          - Logs from Python and Go services land in the same log store and are
            queried by shared dashboards/alerts that key off "message". structlog's
            default is "event"; if the EventRenamer were dropped, Python logs would be
            silently un-queryable under the cross-service schema while still appearing
            to work locally
          - It also pins the canonical timestamp/level fields the schema promises

        **What it tests:**
          - An info("hello") log renders JSON with message=="hello", a present
            "timestamp", and level=="info"
        """
        output = StringIO()
        configure_logging(level="INFO", stream=output)
        import structlog

        structlog.get_logger("t").info("hello")
        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["message"] == "hello"
        assert "timestamp" in data
        assert data["level"] == "info"

    def test_level_filters_below_threshold_events(self) -> None:
        """Test that configure_logging's level actually gates the app's structlog output.

        **Why this test is important:**
          - The Python services flooded staging with DEBUG logs while the configured
            level was info, because the structlog pipeline had no level filter (the level
            only gated stdlib/third-party loggers, not the app's PrintLogger output). This
            pins that the configured level filters the app's own logs, in every
            environment.

        **What it tests:**
          - At level INFO, a .debug() call produces no output while .info() does.
        """
        import structlog

        output = StringIO()
        configure_logging(level="INFO", stream=output)
        log = structlog.get_logger("t")
        log.debug("debug-should-be-filtered")
        log.info("info-should-appear")

        rendered = output.getvalue()
        assert "info-should-appear" in rendered
        assert "debug-should-be-filtered" not in rendered, (
            "DEBUG must be filtered out when the configured level is INFO"
        )


class TestHTTPMetrics:
    """Tests for the HTTP metrics."""

    def test_record_increments(self) -> None:
        """Test that a served request increments the per-request counter for its label set.

        **Why this test is important:**
          - The RED metrics this records (request rate/errors/duration) are the
            backbone of the Grafana dashboards and alerting. If a request failed to
            increment for a given method/route/status, traffic and error rates would
            silently under-report and mask outages

        **What it tests:**
          - Serving GET /healthz with 200 raises the counter sample for that
            method/route/status label combination by exactly 1
        """
        from prometheus_client import REGISTRY

        labels = {"method": "GET", "route": "/healthz", "status": "200"}
        before = REGISTRY.get_sample_value("http_server_requests_total", labels) or 0.0
        _serve("/healthz")
        after = REGISTRY.get_sample_value("http_server_requests_total", labels)
        assert after == before + 1

    def test_metric_names_match_dashboards(self) -> None:
        """Test that the exposed Prometheus metrics use the exact names the dashboards query.

        **Why this test is important:**
          - Metric names are a contract with the Grafana dashboards and alert rules; a
            rename or typo here breaks every panel and alert that selects them, and
            nothing in the service itself would error to flag it. This test is the
            guard that the wire format stays http_server_requests_total /
            http_server_request_duration_seconds

        **What it tests:**
          - The Prometheus exposition text contains both http_server_requests_total
            and http_server_request_duration_seconds
        """
        from prometheus_client import generate_latest

        _serve("/does-not-exist")
        exposed = generate_latest().decode()
        assert "http_server_requests_total" in exposed
        assert "http_server_request_duration_seconds" in exposed

    def test_probe_and_scrape_paths_are_not_logged(self) -> None:
        """Test that health-probe and Prometheus-scrape requests are not access-logged.

        **Why this test is important:**
          - kubelet probes /healthz and /readyz every few seconds and Prometheus
            scrapes /metrics on the same cadence; access-logging all three buries
            the real request logs across every Python service (the /metrics scrape
            alone floods a service's log every 15s)

        **What it tests:**
          - Serving /healthz, /readyz, and /metrics renders no 'http request' line, while a
            normal path still renders exactly one
        """
        out = StringIO()
        configure_logging(level="INFO", stream=out)

        for probe in ("/healthz", "/readyz", "/metrics"):
            _serve(probe)
        assert _http_logs(out.getvalue()) == [], (
            "probe and scrape paths must not be logged"
        )

        _serve("/api/things")
        [entry] = _http_logs(out.getvalue())
        assert entry.get("path") == "/api/things", "normal requests must still be logged"


class TestTracingServerInterceptor:
    """Tests for the tracing server interceptor."""

    @pytest.mark.asyncio
    async def test_passes_through_client_streaming_handlers(self) -> None:
        """Test that the tracing interceptor returns client-streaming handlers untouched.

        **Why this test is important:**
          - The interceptor wraps unary_unary and unary_stream (the shapes our services use;
            server-streaming ``QueryStream`` is unary_stream). It does NOT know how to wrap the
            client-streaming shapes (stream_unary / stream_stream), so it must return them
            unchanged rather than crash or silently break them, while still installing server-wide.

        **What it tests:**
          - For a stream_stream handler (both unary_unary and unary_stream are None),
            intercept_service returns the same handler object it was given (identity)
        """
        interceptor = TracingServerInterceptor("test-svc")

        class _Details(grpc.HandlerCallDetails):
            method = "/svc/Method"
            invocation_metadata = ()

        # A client-streaming handler (unary_unary AND unary_stream are None) is returned as-is.
        non_unary = grpc.stream_stream_rpc_method_handler(lambda req, _ctx: req)
        result = await interceptor.intercept_service(
            AsyncMock(return_value=non_unary), _Details()
        )
        assert result is non_unary

    @pytest.mark.asyncio
    async def test_wraps_server_streaming_handler(self) -> None:
        """Test that the interceptor wraps a server-streaming handler and preserves its chunks.

        **Why this test is important:**
          - The reference RPC ``RetrievalService/QueryStream`` is server-streaming (unary_stream).
            Before this fix the interceptor skipped every non-unary handler, so the whole retrieval
            query path ran with NO active SERVER span — each inner client span started a new root
            trace and single-request correlation was impossible. The interceptor must now wrap the
            stream in one span held open across all yields WITHOUT dropping or reordering chunks.

        **What it tests:**
          - intercept_service returns a handler whose unary_stream is set (the stream is wrapped),
            and iterating it yields the original chunks in order.
        """
        interceptor = TracingServerInterceptor("test-svc")

        async def behavior(request: object, context: object):  # noqa: ANN202, ARG001
            await asyncio.sleep(0)
            for chunk in ("a", "b", "c"):
                yield chunk

        handler = grpc.unary_stream_rpc_method_handler(behavior)

        class _Details(grpc.HandlerCallDetails):
            method = "/svc/Stream"
            invocation_metadata = ()

        wrapped = await interceptor.intercept_service(
            AsyncMock(return_value=handler), _Details()
        )
        assert wrapped is not None
        assert wrapped.unary_stream is not None
        assert wrapped.unary_unary is None
        chunks = [chunk async for chunk in wrapped.unary_stream("req", None)]
        assert chunks == ["a", "b", "c"]

    @pytest.mark.asyncio
    async def test_wraps_unary_handler(self) -> None:
        """Test that the interceptor wraps a unary handler without breaking its behavior.

        **Why this test is important:**
          - Wrapping is where the SERVER span gets created, so the interceptor must
            return a still-valid RPC method handler whose unary_unary callable runs the
            original behavior. A wrapper that lost the response or returned None would
            break every unary RPC while adding tracing

        **What it tests:**
          - intercept_service returns a non-None handler whose unary_unary is callable
            and still yields the original behavior's "ok" result
        """
        interceptor = TracingServerInterceptor("test-svc")

        async def behavior(request: object, context: object) -> str:  # noqa: ARG001
            await asyncio.sleep(0)
            return "ok"

        handler = grpc.unary_unary_rpc_method_handler(behavior)

        class _Details(grpc.HandlerCallDetails):
            method = "/svc/Method"
            invocation_metadata = ()

        wrapped = await interceptor.intercept_service(
            AsyncMock(return_value=handler), _Details()
        )
        assert wrapped is not None
        assert wrapped.unary_unary is not None
        assert await wrapped.unary_unary("req", None) == "ok"


class TestSampleRate:
    """Tests for the sample rate."""

    @pytest.mark.parametrize(
        ("raw", "expected"),
        [
            (None, 1.0),  # unset -> sample everything
            ("", 1.0),  # empty -> default
            ("0.1", 0.1),  # honoured (matches the Go TRACE_SAMPLE_RATE)
            ("1", 1.0),
            ("0", 0.0),
            ("abc", 1.0),  # invalid -> default, never silently disables tracing
            ("5", 1.0),  # above range -> clamped to 1.0
            ("-0.5", 0.0),  # below range -> clamped to 0.0
        ],
    )
    def test_parse_sample_rate(self, raw: str | None, expected: float) -> None:
        """Test that TRACE_SAMPLE_RATE parses to a clamped 0.0-1.0 fraction defaulting to 1.0.

        **Why this test is important:**
          - This value controls how much of production traffic is traced. Two failure
            modes are costly: an unset/invalid config that silently disables tracing
            (blinding ops) and an out-of-range value that the SDK rejects. The parser
            must fail safe to 1.0 on unset/empty/garbage and clamp numbers into range,
            matching the Go services' TRACE_SAMPLE_RATE semantics so both stacks agree

        **What it tests:**
          - None/""/"abc" -> 1.0 (fail open), valid in-range values pass through,
            "5" clamps to 1.0 and "-0.5" clamps to 0.0
        """
        assert parse_sample_rate(raw) == expected


class TestHTTPRequestLogging:
    """The 'http request' log uses a status-appropriate level and fires on error paths.

    Regression: the aiohttp adapter logged nothing on 4xx/5xx (only
    the success path logged), and both adapters logged everything at info.
    """

    @staticmethod
    def _last_http_log(raw: str) -> dict[str, object]:
        entries = [json.loads(line) for line in raw.strip().split("\n") if line]
        http = [e for e in entries if e.get("message") == "http request"]
        return http[-1] if http else {}

    def test_aiohttp_404_logs_at_warning(self) -> None:
        """Test that a 404 on the aiohttp adapter emits an http-request log at warning level.

        **Why this test is important:**
          - This is a direct regression guard: the aiohttp adapter previously logged
            nothing on the HTTPException (4xx/5xx) path, so error traffic vanished from
            the logs. The fix must both fire the "http request" log on the error path
            and use a status-appropriate level so 4xx is greppable as a warning, not
            buried at info or dropped entirely

        **What it tests:**
          - A handler raising HTTPNotFound produces a final "http request" log with
            status==404 and level=="warning"
        """
        from aiohttp import web
        from aiohttp.test_utils import make_mocked_request

        out = StringIO()
        configure_logging(level="INFO", stream=out)
        middleware = http_observability.aiohttp_observability_middleware("test-svc")

        async def handler(_request: object) -> object:
            await asyncio.sleep(0)
            raise web.HTTPNotFound

        with pytest.raises(web.HTTPNotFound):
            asyncio.run(
                middleware(make_mocked_request("GET", "/does-not-exist"), handler)
            )

        log = self._last_http_log(out.getvalue())
        assert log.get("status") == 404
        assert log.get("level") == "warning"

    def test_aiohttp_unhandled_exception_logs_error_with_traceback(self) -> None:
        """Test that an unhandled handler exception logs at error with a structured traceback.

        **Why this test is important:**
          - A crash inside a handler is the highest-signal log there is; it must be
            recorded as a 500 at error level and carry the traceback so on-call can
            diagnose without reproducing. Logging it at info, omitting the exception,
            or swallowing the re-raise would hide real outages

        **What it tests:**
          - A handler raising ValueError yields a final "http request" log with
            status==500, level=="error", and an "exception" field present
        """
        from aiohttp.test_utils import make_mocked_request

        out = StringIO()
        configure_logging(level="INFO", stream=out)
        middleware = http_observability.aiohttp_observability_middleware("test-svc")

        async def handler(_request: object) -> object:
            await asyncio.sleep(0)
            msg = "boom"
            raise ValueError(msg)

        with pytest.raises(ValueError, match="boom"):
            asyncio.run(
                middleware(make_mocked_request("GET", "/does-not-exist"), handler)
            )

        log = self._last_http_log(out.getvalue())
        assert log.get("status") == 500
        assert log.get("level") == "error"
        assert "exception" in log

    def test_fastapi_404_logs_at_warning(self) -> None:
        """Test that a 404 response on the FastAPI adapter logs at warning, not info.

        **Why this test is important:**
          - The FastAPI/Starlette adapter is a separate code path from aiohttp but must
            honor the same status-to-level mapping; the original bug logged everything
            at info on both adapters. Pinning the FastAPI 4xx path keeps log severity
            consistent across frameworks so dashboards and alerts behave identically
            regardless of which service emitted the line

        **What it tests:**
          - A call_next returning a 404 response produces a final "http request" log
            with status==404 and level=="warning"
        """
        from starlette.requests import Request
        from starlette.responses import PlainTextResponse, Response

        out = StringIO()
        configure_logging(level="INFO", stream=out)

        class _App:
            title = "test-svc"

        scope = {
            "type": "http",
            "method": "GET",
            "path": "/does-not-exist",
            "headers": [],
            "query_string": b"",
            "app": _App(),
        }

        async def call_next(_request: Request) -> Response:
            await asyncio.sleep(0)
            return PlainTextResponse("nope", status_code=404)

        asyncio.run(
            http_observability.fastapi_observability_middleware(Request(scope), call_next)
        )

        log = self._last_http_log(out.getvalue())
        assert log.get("status") == 404
        assert log.get("level") == "warning"
