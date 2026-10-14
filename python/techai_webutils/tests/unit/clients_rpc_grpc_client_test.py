"""Tests for the RPC client tier: a channel's calls run through ``client_interceptors``.

The end-to-end cases call a generic (bytes-in, bytes-out) service on an in-process ``grpc.aio``
server over loopback, so retries, metadata and statuses cross a real wire.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import MagicMock

import grpc
import pytest
from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider

from techai_webutils.clients.client import ClientBase
from techai_webutils.clients.rpc import RpcConfig, RpcKind, new_rpc_channel_from_config
from techai_webutils.clients.rpc.grpc import (
    ChannelConfig,
    client_interceptors,
    create_channel,
)
from techai_webutils.clients.rpc.grpc.interceptors import to_rpc_error
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.foundation.decorate import Deps
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError
from techai_webutils.foundation.resilience.retrier import (
    ExponentialRetrier,
    ExponentialRetrierConfig,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Callable


class _Service:
    """A generic test service recording the metadata of every call."""

    def __init__(self, failures: int = 0) -> None:
        """Fail ``Flaky`` UNAVAILABLE ``failures`` times before it succeeds."""
        self.failures = failures
        self.calls = 0
        self.metadata: list[dict[str, str]] = []

    def _record(self, context: grpc.aio.ServicerContext[bytes, bytes]) -> None:
        """Record the call's invocation metadata."""
        self.calls += 1
        self.metadata.append({k: str(v) for k, v in context.invocation_metadata() or ()})

    async def flaky(
        self, request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
    ) -> bytes:
        """Answer the request after ``failures`` UNAVAILABLE aborts."""
        self._record(context)
        if self.calls <= self.failures:
            await context.abort(grpc.StatusCode.UNAVAILABLE, "try again")
        return request

    async def missing(
        self, _request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
    ) -> bytes:
        """Abort NOT_FOUND."""
        self._record(context)
        await context.abort(grpc.StatusCode.NOT_FOUND, "no such thing")
        return b""  # pragma: no cover - abort raises

    async def stream(
        self, request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
    ) -> AsyncIterator[bytes]:
        """Stream two chunks."""
        self._record(context)
        await asyncio.sleep(0)
        yield request
        yield request


async def _serve(
    service: _Service,
    run: Callable[[grpc.aio.Channel], object],
    base: ClientBase,
    token: str = "",
) -> object:
    """Serve ``service``, open a channel through the client tier and return ``run``'s result."""
    server = grpc.aio.server()
    handlers = {
        "Flaky": grpc.unary_unary_rpc_method_handler(service.flaky),
        "Missing": grpc.unary_unary_rpc_method_handler(service.missing),
        "Stream": grpc.unary_stream_rpc_method_handler(service.stream),
    }
    server.add_generic_rpc_handlers((
        grpc.method_handlers_generic_handler("test.Svc", handlers),
    ))
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    channel = create_channel(
        f"127.0.0.1:{port}", interceptors=client_interceptors(base, token)
    )
    try:
        return await run(channel)  # type: ignore[misc]
    finally:
        await channel.close()
        await server.stop(None)


def _retrying() -> ClientBase:
    """Return a client base that retries transient failures without waiting."""
    retrier = ExponentialRetrier(
        ExponentialRetrierConfig(initial_interval=0.0, max_interval=0.0)
    )
    return ClientBase("rpc", Deps(retrier=retrier))


@pytest.mark.asyncio
async def test_a_transient_status_is_retried_through_the_tier() -> None:
    """An UNAVAILABLE answer is retried; the caller sees the eventual response.

    Why this test is important:
      - The client tier's retry layer must see a coded transient error, or a brief endpoint flap
        fails the caller.

    What it tests:
      - With two UNAVAILABLE answers, the call is attempted three times and returns the response.
    """
    service = _Service(failures=2)

    async def run(channel: grpc.aio.Channel) -> bytes:
        return await channel.unary_unary("/test.Svc/Flaky")(b"hi")

    assert await _serve(service, run, _retrying()) == b"hi"
    assert service.calls == 3


@pytest.mark.asyncio
async def test_a_server_status_reaches_the_caller_unchanged() -> None:
    """A permanent server status is not retried and reaches the caller as the server sent it.

    Why this test is important:
      - Callers branch on the gRPC status; the tier must not rewrite it.

    What it tests:
      - ``Missing`` raises an ``AioRpcError`` NOT_FOUND with the server's message after one call.
    """
    service = _Service()

    async def run(channel: grpc.aio.Channel) -> bytes:
        return await channel.unary_unary("/test.Svc/Missing")(b"hi")

    with pytest.raises(grpc.aio.AioRpcError) as caught:
        await _serve(service, run, _retrying())

    assert caught.value.code() == grpc.StatusCode.NOT_FOUND
    assert caught.value.details() == "no such thing"
    assert service.calls == 1


@pytest.mark.asyncio
async def test_a_tier_rejection_becomes_the_status_of_its_code() -> None:
    """A failure the tier produced itself (an open breaker) reaches the caller as a gRPC status.

    Why this test is important:
      - A stub caller handles ``AioRpcError``; a bare ``AppError`` from the tier would escape
        every caller's error handling.

    What it tests:
      - With an open breaker the call never reaches the server and raises UNAVAILABLE.
    """
    breaker = MagicMock(spec=CircuitBreakerInterface)
    breaker.__enter__.side_effect = CircuitOpenError("rpc")
    service = _Service()

    async def run(channel: grpc.aio.Channel) -> bytes:
        return await channel.unary_unary("/test.Svc/Flaky")(b"hi")

    with pytest.raises(grpc.aio.AioRpcError) as caught:
        await _serve(service, run, ClientBase("rpc", Deps(breaker=breaker)))

    assert caught.value.code() == grpc.StatusCode.UNAVAILABLE
    assert service.calls == 0


@pytest.mark.asyncio
async def test_every_call_carries_the_trace_and_the_service_token() -> None:
    """Unary and streaming calls carry the active trace context and the bearer token.

    Why this test is important:
      - The server continues the caller's trace only if the context crosses the wire, and a
        server with service auth rejects a call without the token.

    What it tests:
      - Inside an active span, both calls carry a ``traceparent`` with the span's trace id and
        ``authorization: Bearer tok``; the stream yields its chunks.
    """
    service = _Service()
    tracer = TracerProvider().get_tracer("test")

    async def run(channel: grpc.aio.Channel) -> list[bytes]:
        await channel.unary_unary("/test.Svc/Flaky")(b"hi")
        return [chunk async for chunk in channel.unary_stream("/test.Svc/Stream")(b"s")]

    with tracer.start_as_current_span("caller") as span:
        trace_id = format(span.get_span_context().trace_id, "032x")
        chunks = await _serve(service, run, ClientBase("rpc", Deps()), token="tok")

    assert chunks == [b"s", b"s"]
    for metadata in service.metadata:
        assert trace_id in metadata["traceparent"]
        assert metadata["authorization"] == "Bearer tok"
    assert trace.get_current_span() is not span


def test_to_rpc_error_maps_a_tier_failure_to_its_code() -> None:
    """A coded failure with no server status becomes an ``AioRpcError`` of its code.

    Why this test is important:
      - The status a caller sees must follow the shared error-code table.

    What it tests:
      - A RESOURCE_EXHAUSTED ``AppError`` becomes RESOURCE_EXHAUSTED with its message.
    """
    err = to_rpc_error(AppError(ErrorCode.RESOURCE_EXHAUSTED, "bulkhead full"))
    assert err.code() == grpc.StatusCode.RESOURCE_EXHAUSTED
    assert err.details() == "bulkhead full"


@pytest.mark.asyncio
async def test_new_rpc_channel_from_config_decorates_and_rejects_an_unknown_kind() -> (
    None
):
    """The factory returns a decorated channel without I/O and fails loudly on an unknown kind.

    Why this test is important:
      - The composition root builds the channel at boot with zero infrastructure; a typo in the
        kind must not silently pick a backend.

    What it tests:
      - The gRPC kind returns a ``grpc.aio.Channel``; an unknown kind raises ``AppValueError``.
    """
    config = RpcConfig(
        name="retrieval", service_token="t", channel=ChannelConfig("127.0.0.1:1")
    )
    channel = new_rpc_channel_from_config(config)
    assert isinstance(channel, grpc.aio.Channel)
    await channel.close()
    with pytest.raises(AppValueError, match="unknown rpc kind"):
        new_rpc_channel_from_config(RpcConfig(kind="kafka"))  # type: ignore[arg-type]
    assert RpcKind("grpc") is RpcKind.GRPC
