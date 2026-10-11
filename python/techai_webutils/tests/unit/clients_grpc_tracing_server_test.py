"""Tests for the gRPC TracingServerInterceptor's server-streaming span lifecycle."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import MagicMock, patch

import grpc
import pytest
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.trace import StatusCode

from techai_webutils.clients.rpc.grpc.interceptors.tracing_server import (
    TracingServerInterceptor,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from opentelemetry.trace import Tracer

_TRACER_PATH = (
    "techai_webutils.clients.rpc.grpc.interceptors.tracing_server.otel_trace.get_tracer"
)


def _real_span_tracer() -> tuple[Tracer, InMemorySpanExporter]:
    """Return a real OTel tracer backed by an in-memory exporter, so a test sees genuine span contexts."""
    provider = TracerProvider()
    exporter = InMemorySpanExporter()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    return provider.get_tracer("test"), exporter


def _stream_handler(tracer: Tracer) -> grpc.RpcMethodHandler[object, object]:
    """Return a unary-stream handler that yields chunks forever, so a test can cancel it mid-stream.

    Each chunk is produced inside a ``produce`` child span started from the current context,
    the way a handler's client calls start their spans.
    """

    async def endless(_request: object, _context: object) -> AsyncIterator[str]:
        await asyncio.sleep(0)
        index = 0
        while True:
            with tracer.start_as_current_span("produce"):
                chunk = f"chunk-{index}"
                index += 1
            yield chunk

    handler = MagicMock(spec=grpc.RpcMethodHandler)
    handler.unary_unary = None
    handler.unary_stream = endless
    handler.request_deserializer = None
    handler.response_serializer = None
    return handler


class TestTracingServerInterceptorStreaming:
    """Tests for the tracing server interceptor on server-streaming RPCs."""

    @pytest.mark.asyncio
    async def test_server_span_closed_when_stream_cancelled_midway(self) -> None:
        """A client that cancels mid-stream still ends the SERVER span, without an ERROR status.

        Why this test is important:
          - The SERVER span covers the whole server-streaming response. If grpc.aio abandons
            the generator on a mid-stream client cancel (aclose/GC finalisation) rather than
            driving it to StopAsyncIteration, a naive implementation could leak the span. This
            locks in that the span is always ended — and, because cancellation is not an error,
            that it is NOT recorded as ERROR.
          - The spans the handler starts while producing chunks must stay children of the
            SERVER span, so one streamed request stays one trace.

        What it tests:
          - After the first yielded chunk the SERVER span is still open, and the handler's
            ``produce`` span is its child; calling ``aclose()`` on the response generator (the
            client-cancel path) ends the SERVER span with an UNSET status.
        """
        tracer, exporter = _real_span_tracer()
        with patch(_TRACER_PATH, return_value=tracer):
            interceptor = TracingServerInterceptor("test")

        async def continuation(_details: object) -> grpc.RpcMethodHandler[object, object]:
            await asyncio.sleep(0)
            return _stream_handler(tracer)

        details = MagicMock(spec=grpc.HandlerCallDetails)
        details.method = "/test.RetrievalService/QueryStream"
        details.invocation_metadata = []

        wrapped = await interceptor.intercept_service(continuation, details)
        gen = wrapped.unary_stream(object(), MagicMock())

        assert await anext(gen) == "chunk-0"
        finished = {span.name: span for span in exporter.get_finished_spans()}
        assert set(finished) == {"produce"}  # the SERVER span is still open mid-stream

        await (
            gen.aclose()
        )  # client cancels + abandons the stream (aclose → GeneratorExit)

        server = next(
            span for span in exporter.get_finished_spans() if span.name == details.method
        )
        assert server.status.status_code is StatusCode.UNSET  # a cancel is not an error
        produce_parent = finished["produce"].parent
        assert produce_parent is not None
        assert server.context is not None
        assert produce_parent.span_id == server.context.span_id


class TestTracingServerInterceptorTraceResponse:
    """Tests for the tracing server interceptor's trace-response span."""

    @pytest.mark.asyncio
    async def test_unary_returns_traceresponse_trailing_metadata(self) -> None:
        """A unary RPC returns the active span's trace id to the caller as ``traceresponse`` trailing metadata.

        Why this test is important:
          - A telemetry round-trip proof needs the caller to learn its trace id;
            for the internal gRPC servers that means trailing metadata — the gRPC analogue of the Go
            connect ``traceresponse`` header. Without it an internal call cannot be correlated to its spans.

        What it tests:
          - After the handler runs, ``set_trailing_metadata`` carries a ``traceresponse`` in W3C
            traceparent format whose trace id equals the SERVER span's trace id.
        """
        tracer, exporter = _real_span_tracer()
        with patch(_TRACER_PATH, return_value=tracer):
            interceptor = TracingServerInterceptor("test")

        async def inner_unary(_request: object, _context: object) -> str:
            await asyncio.sleep(0)
            return "ok"

        handler = MagicMock(spec=grpc.RpcMethodHandler)
        handler.unary_unary = inner_unary
        handler.unary_stream = None
        handler.request_deserializer = None
        handler.response_serializer = None

        async def continuation(_details: object) -> grpc.RpcMethodHandler[object, object]:
            await asyncio.sleep(0)
            return handler

        details = MagicMock(spec=grpc.HandlerCallDetails)
        details.method = "/test.TestService/Do"
        details.invocation_metadata = []

        wrapped = await interceptor.intercept_service(continuation, details)
        context = MagicMock()
        result = await wrapped.unary_unary(object(), context)

        assert result == "ok"
        context.set_trailing_metadata.assert_called_once()
        metadata = dict(context.set_trailing_metadata.call_args[0][0])
        traceparent = metadata["traceresponse"]
        assert traceparent.startswith("00-")

        spans = exporter.get_finished_spans()
        assert len(spans) == 1
        span_context = spans[0].context
        assert span_context is not None
        span_trace_id = format(span_context.trace_id, "032x")
        assert span_trace_id in traceparent
