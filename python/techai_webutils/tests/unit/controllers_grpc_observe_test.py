"""Tests for the gRPC-native observability interceptors of the controller tier.

The tracing, metrics and logging server interceptors run around the whole RPC — a stream
included — and read the RPC's final status, the way the Go gRPC server's interceptors do.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import pytest
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.trace import StatusCode

from techai_webutils.controllers.grpc.interceptors.observe import (
    LoggingServerInterceptor,
    MetricsServerInterceptor,
    TracingServerInterceptor,
    parse_method,
    status_name,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

    from opentelemetry.trace import Tracer

_TRACER_PATH = (
    "techai_webutils.controllers.grpc.interceptors.observe.otel_trace.get_tracer"
)
"""Where the tracing interceptor acquires its OTel tracer."""


def _real_span_tracer() -> tuple[Tracer, InMemorySpanExporter]:
    """Return a real OTel tracer backed by an in-memory exporter."""
    provider = TracerProvider()
    exporter = InMemorySpanExporter()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    return provider.get_tracer("test"), exporter


def _details(method: str = "/test.TestService/Do") -> grpc.HandlerCallDetails:
    """Handler-call-details for ``method`` with no metadata."""
    details = MagicMock(spec=grpc.HandlerCallDetails)
    details.method = method
    details.invocation_metadata = []
    return details


def _context(code: grpc.StatusCode | None = None) -> MagicMock:
    """Return a servicer context whose final status is ``code`` and whose ``write`` is awaitable."""
    context = MagicMock()
    context.code.return_value = code
    context.write = AsyncMock()
    return context


async def _wrap(
    interceptor: grpc.aio.ServerInterceptor,
    handler: grpc.RpcMethodHandler[object, object],
) -> grpc.RpcMethodHandler[object, object]:
    """Run ``handler`` through ``interceptor`` and return the rebuilt handler."""
    wrapped = await interceptor.intercept_service(
        AsyncMock(return_value=handler), _details()
    )
    assert wrapped is not None
    return wrapped


def _behavior(
    handler: grpc.RpcMethodHandler[object, object], *, stream: bool = False
) -> Callable[[object, object], Awaitable[object]]:
    """Return a rebuilt handler's behavior; grpc's stubs type it as synchronous."""
    fn = handler.unary_stream if stream else handler.unary_unary
    return cast("Callable[[object, object], Awaitable[object]]", fn)


def test_parse_method_and_status_name() -> None:
    """Methods split into service and method, and statuses are named as Go names them.

    Why this test is important:
      - The series labels and the access line must match the Go server's, so one dashboard
        serves both languages.

    What it tests:
      - ``/pkg.Svc/Do`` splits into (``pkg.Svc``, ``Do``); a bare name has no service.
      - No error is ``OK``; an abort's code is CamelCased (``ResourceExhausted``), CANCELLED is
        ``Canceled``, and an error with no status set is ``Unknown``.
    """
    assert parse_method("/pkg.Svc/Do") == ("pkg.Svc", "Do")
    assert parse_method("Do") == ("", "Do")
    assert status_name(_context(), None) == "OK"
    exhausted = _context(grpc.StatusCode.RESOURCE_EXHAUSTED)
    assert status_name(exhausted, RuntimeError()) == "ResourceExhausted"
    assert status_name(_context(grpc.StatusCode.CANCELLED), RuntimeError()) == "Canceled"
    assert status_name(_context(None), RuntimeError()) == "Unknown"


class TestTracingServerInterceptor:
    """The SERVER span of each RPC."""

    @pytest.mark.asyncio
    async def test_unary_span_continues_caller_trace_and_returns_traceresponse(
        self,
    ) -> None:
        """A unary RPC runs in a SERVER span parented on the caller's trace, echoed back.

        Why this test is important:
          - One request must stay one trace across services, and a caller learns its trace id
            from the ``traceresponse`` trailer.

        What it tests:
          - The span is named by the method, carries the rpc attributes, has the caller's trace
            id, and the trailer carries that trace id.
        """
        tracer, exporter = _real_span_tracer()
        with patch(_TRACER_PATH, return_value=tracer):
            interceptor = TracingServerInterceptor("test")
        trace_id = "0af7651916cd43dd8448eb211c80319c"
        details = _details()
        details.invocation_metadata = [
            ("traceparent", f"00-{trace_id}-b7ad6b7169203331-01"),
        ]
        handler = grpc.unary_unary_rpc_method_handler(AsyncMock(return_value="ok"))
        wrapped = await interceptor.intercept_service(
            AsyncMock(return_value=handler), details
        )
        assert wrapped is not None
        context = _context()

        assert await _behavior(wrapped)(object(), context) == "ok"

        [span] = exporter.get_finished_spans()
        assert span.name == "/test.TestService/Do"
        assert span.attributes is not None
        assert span.attributes["rpc.service"] == "test.TestService"
        assert span.attributes["rpc.method"] == "Do"
        assert span.context is not None
        assert format(span.context.trace_id, "032x") == trace_id
        trailer = dict(context.set_trailing_metadata.call_args.args[0])
        assert trace_id in trailer["traceresponse"]

    @pytest.mark.asyncio
    async def test_stream_span_spans_the_whole_stream_and_cancel_is_not_an_error(
        self,
    ) -> None:
        """A server stream runs in one span, its chunks' spans its children; a cancel ends it cleanly.

        Why this test is important:
          - The server-streaming query path must be one trace, and a client hanging up is not a
            server error.

        What it tests:
          - Chunks are written in order inside the span; the span ends when the client cancels
            mid-stream, with an UNSET status, and the handler's spans are its children.
        """
        tracer, exporter = _real_span_tracer()
        with patch(_TRACER_PATH, return_value=tracer):
            interceptor = TracingServerInterceptor("test")

        async def endless(_request: object, _context: object) -> AsyncIterator[str]:
            index = 0
            while True:
                await asyncio.sleep(0)
                with tracer.start_as_current_span("produce"):
                    chunk = f"chunk-{index}"
                    index += 1
                yield chunk

        wrapped = await _wrap(interceptor, grpc.unary_stream_rpc_method_handler(endless))
        context = _context()
        context.write.side_effect = [None, asyncio.CancelledError()]

        with pytest.raises(asyncio.CancelledError):
            await _behavior(wrapped, stream=True)(object(), context)

        assert [c.args[0] for c in context.write.await_args_list] == [
            "chunk-0",
            "chunk-1",
        ]
        spans = {span.name: span for span in exporter.get_finished_spans()}
        server = spans["/test.TestService/Do"]
        assert server.status.status_code is StatusCode.UNSET
        produce_parent = spans["produce"].parent
        assert produce_parent is not None
        assert server.context is not None
        assert produce_parent.span_id == server.context.span_id

    @pytest.mark.asyncio
    async def test_failure_marks_the_span_error(self) -> None:
        """A failed RPC records an ERROR status on its span.

        Why this test is important:
          - A trace search for failed requests relies on the span status.

        What it tests:
          - A handler exception propagates and the span's status is ERROR.
        """
        tracer, exporter = _real_span_tracer()
        with patch(_TRACER_PATH, return_value=tracer):
            interceptor = TracingServerInterceptor("test")
        failing = AsyncMock(side_effect=RuntimeError("x"))
        wrapped = await _wrap(interceptor, grpc.unary_unary_rpc_method_handler(failing))

        with pytest.raises(RuntimeError):
            await _behavior(wrapped)(object(), _context())

        [span] = exporter.get_finished_spans()
        assert span.status.status_code is StatusCode.ERROR

    @pytest.mark.asyncio
    async def test_unknown_method_passes_through(self) -> None:
        """A method with no handler stays unhandled, so gRPC answers UNIMPLEMENTED.

        Why this test is important:
          - Wrapping a missing handler would crash the server instead of rejecting the call.

        What it tests:
          - intercept_service returns None when the continuation does.
        """
        interceptor = TracingServerInterceptor("test")
        continuation = AsyncMock(return_value=None)
        assert await interceptor.intercept_service(continuation, _details()) is None


class TestMetricsServerInterceptor:
    """The request count and duration series."""

    @pytest.mark.asyncio
    async def test_counts_by_final_status(self) -> None:
        """Each RPC is counted under its final status and timed.

        Why this test is important:
          - Error-rate alerts divide the non-OK count by the total; a wrong code label hides
            failures.

        What it tests:
          - The Go-parity series are created; an OK call counts ``code=OK``, an aborted call
            counts its status, and both observe a duration.
        """
        metrics, requests, duration = MagicMock(), MagicMock(), MagicMock()
        metrics.counter.return_value = requests
        metrics.histogram.return_value = duration
        interceptor = MetricsServerInterceptor(metrics)
        assert metrics.counter.call_args.args[0] == "grpc_server_requests_total"
        assert metrics.histogram.call_args.args[0] == "grpc_server_duration_seconds"

        ok = await _wrap(interceptor, grpc.unary_unary_rpc_method_handler(AsyncMock()))
        await _behavior(ok)(object(), _context())
        abort = AsyncMock(side_effect=grpc.aio.AbortError("x"))
        aborted = await _wrap(interceptor, grpc.unary_unary_rpc_method_handler(abort))
        with pytest.raises(grpc.aio.AbortError):
            await _behavior(aborted)(object(), _context(grpc.StatusCode.NOT_FOUND))

        codes = [c.kwargs["code"] for c in requests.inc.call_args_list]
        assert codes == ["OK", "NotFound"]
        assert requests.inc.call_args.kwargs["service"] == "test.TestService"
        assert duration.observe.call_count == 2


class TestLoggingServerInterceptor:
    """The access line of each RPC."""

    @pytest.mark.asyncio
    async def test_logs_completion_and_failure(self) -> None:
        """A completed RPC logs at info and a failed one at error, with its status.

        Why this test is important:
          - The access line is the one production request log of a gRPC service.

        What it tests:
          - A success logs ``gRPC request completed`` with ``code=OK``; a failure logs
            ``gRPC request failed`` with the status name and re-raises.
        """
        logger = MagicMock()
        interceptor = LoggingServerInterceptor(logger)

        ok = await _wrap(interceptor, grpc.unary_unary_rpc_method_handler(AsyncMock()))
        await _behavior(ok)(object(), _context())
        failing = AsyncMock(side_effect=RuntimeError("x"))
        failed = await _wrap(interceptor, grpc.unary_unary_rpc_method_handler(failing))
        with pytest.raises(RuntimeError):
            await _behavior(failed)(object(), _context(grpc.StatusCode.INTERNAL))

        assert logger.info.call_args.args[0] == "gRPC request completed"
        assert logger.info.call_args.kwargs["code"] == "OK"
        assert logger.error.call_args.args[0] == "gRPC request failed"
        assert logger.error.call_args.kwargs["code"] == "Internal"
