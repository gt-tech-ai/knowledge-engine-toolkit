"""Tests for the RPC client tier: a channel's calls run through ``client_interceptors``.

The end-to-end cases call a generic (bytes-in, bytes-out) service on an in-process
``grpc.aio`` server over loopback, so retries, metadata and statuses cross a real wire.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, NoReturn, cast, final
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
from techai_webutils.foundation.decorate import Decorations, Deps
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError
from techai_webutils.foundation.resilience.retrier import (
    ExponentialRetrier,
    ExponentialRetrierConfig,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

_FAKE_CRED_1 = "tok"
_FAKE_CRED_2 = "t"

_EXPECTED_CALLS = 3
_EXPECTED_CALLS_2 = 2


_UNDECODABLE = b"\xff"
"""A reply ``_decode`` rejects, standing in for a corrupt protobuf message on the wire."""


def _decode(data: bytes) -> bytes:
    """Deserialize a reply.

    Rejecting ``_UNDECODABLE`` the way a protobuf parse rejects corrupt bytes.

    Args:
        data: The bytes.

    Returns:
        The reply.

    Raises:
        ValueError: Wire format was corrupt.

    """
    if data == _UNDECODABLE:
        msg = "Wire format was corrupt"
        raise ValueError(msg)
    return data


@final
class _Service:
    """A generic test service recording the metadata of every call."""

    def __init__(self, failures: int = 0) -> None:
        """Fail ``Flaky`` UNAVAILABLE ``failures`` times before it succeeds.

        Args:
            failures: How many calls fail before one succeeds.

        """
        self.failures = failures
        self.calls = 0
        self.metadata: list[dict[str, str]] = []

    def _record(self, context: grpc.aio.ServicerContext[bytes, bytes]) -> None:
        """Record the call's invocation metadata.

        Args:
            context: The call's context.

        """
        self.calls += 1
        self.metadata.append({k: str(v) for k, v in context.invocation_metadata() or ()})

    async def flaky(
        self, request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
    ) -> bytes:
        """Answer the request after ``failures`` UNAVAILABLE aborts.

        Args:
            request: The test's pytest request.
            context: The call's context.

        Returns:
            The request.

        """
        self._record(context)
        if self.calls <= self.failures:
            await context.abort(grpc.StatusCode.UNAVAILABLE, "try again")
        return request

    async def garbled(
        self, request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
    ) -> bytes:
        """Answer an undecodable reply on the first call, then echo the request.

        Args:
            request: The test's pytest request.
            context: The call's context.

        Returns:
            The undecodable reply on the first call, then the request.

        """
        self._record(context)
        return _UNDECODABLE if self.calls == 1 else request

    async def missing(
        self, _request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
    ) -> NoReturn:
        """Abort NOT_FOUND; ``abort`` raises, so this never returns.

        Args:
            _request: Unused.
            context: The call's context.

        """
        self._record(context)
        await context.abort(grpc.StatusCode.NOT_FOUND, "no such thing")

    async def stream(
        self, request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
    ) -> AsyncIterator[bytes]:
        """Stream two chunks.

        Args:
            request: The test's pytest request.
            context: The call's context.

        Yields:
            Each chunk.

        """
        self._record(context)
        await asyncio.sleep(0)
        yield request
        yield request


async def _serve(
    service: _Service,
    run: Callable[[grpc.aio.Channel], Awaitable[object]],
    base: ClientBase,
    token: str = "",
) -> object:
    """Serve ``service``.

    Open a channel through the client tier and return ``run``'s result.

    Args:
        service: The service to serve.
        run: Runs against the channel.
        base: The client tier.
        token: The service token; ``None`` sends none.

    Returns:
        What ``run`` returns.

    """
    server = grpc.aio.server()
    handlers = {
        "Flaky": grpc.unary_unary_rpc_method_handler(service.flaky),
        "Garbled": grpc.unary_unary_rpc_method_handler(service.garbled),
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
        return await run(channel)
    finally:
        await channel.close()
        await server.stop(None)


def _retrying() -> ClientBase:
    """Return a client base that retries transient failures without waiting.

    Returns:
        A client base that retries transient failures without waiting.

    """
    retrier = ExponentialRetrier(
        ExponentialRetrierConfig(initial_interval=0.0, max_interval=0.0)
    )
    return ClientBase.wire(Decorations.fixed(Deps(retrier=retrier)), "rpc")


@pytest.mark.asyncio
async def test_a_transient_status_is_retried_through_the_tier() -> None:
    """An UNAVAILABLE answer is retried; the caller sees the eventual response.

    Why this test is important:
      - The client tier's retry layer must see a coded transient error, or a brief
        endpoint flap fails the caller.

    What it tests:
      - With two UNAVAILABLE answers, the call is attempted three times and returns the
        response.
    """
    service = _Service(failures=2)

    async def run(channel: grpc.aio.Channel) -> bytes:
        """Call the flaky method.

        Args:
            channel: The client channel.

        Returns:
            The reply.

        """
        return await channel.unary_unary("/test.Svc/Flaky")(b"hi")

    assert await _serve(service, run, _retrying()) == b"hi"
    assert service.calls == _EXPECTED_CALLS


@pytest.mark.asyncio
async def test_an_undecodable_reply_is_retried_instead_of_returning_none() -> None:
    """A reply grpc-python cannot deserialize is retried.

    Never handed to the caller as None.

    Why this test is important:
      - grpc-python logs "Exception deserializing message!" and resolves the call to None
        when a reply does not parse; the caller then fails on ``None.<field>`` with no
        retry and no sign of which RPC broke.

    What it tests:
      - With one undecodable reply, the call is attempted twice and returns the second
        reply.
    """
    service = _Service()

    async def run(channel: grpc.aio.Channel) -> bytes:
        """Call the garbled method with a strict decoder.

        Args:
            channel: The client channel.

        Returns:
            The reply.

        """
        call = channel.unary_unary("/test.Svc/Garbled", response_deserializer=_decode)
        return await call(b"hi")

    assert await _serve(service, run, _retrying()) == b"hi"
    assert service.calls == _EXPECTED_CALLS_2


@pytest.mark.asyncio
async def test_a_server_status_reaches_the_caller_unchanged() -> None:
    """A permanent server status is not retried and reaches the caller.

    As the server sent it.

    Why this test is important:
      - Callers branch on the gRPC status; the tier must not rewrite it.

    What it tests:
      - ``Missing`` raises an ``AioRpcError`` NOT_FOUND with the server's message after
        one call.
    """
    service = _Service()

    async def run(channel: grpc.aio.Channel) -> bytes:
        """Call the missing method.

        Args:
            channel: The client channel.

        Returns:
            The reply.

        """
        return await channel.unary_unary("/test.Svc/Missing")(b"hi")

    with pytest.raises(grpc.aio.AioRpcError) as caught:
        _ = await _serve(service, run, _retrying())

    assert caught.value.code() == grpc.StatusCode.NOT_FOUND
    assert caught.value.details() == "no such thing"
    assert service.calls == 1


@pytest.mark.asyncio
async def test_a_tier_rejection_becomes_the_status_of_its_code() -> None:
    """A failure the tier produced itself.

    (an open breaker) reaches the caller as a gRPC status.

    Why this test is important:
      - A stub caller handles ``AioRpcError``; a bare ``AppError`` from the tier would
        escape every caller's error handling.

    What it tests:
      - With an open breaker the call never reaches the server and raises UNAVAILABLE.
    """
    breaker = MagicMock(spec=CircuitBreakerInterface)
    cast("MagicMock", breaker.__enter__).side_effect = CircuitOpenError("rpc")
    service = _Service()

    async def run(channel: grpc.aio.Channel) -> bytes:
        """Call the flaky method.

        Args:
            channel: The client channel.

        Returns:
            The reply.

        """
        return await channel.unary_unary("/test.Svc/Flaky")(b"hi")

    with pytest.raises(grpc.aio.AioRpcError) as caught:
        _ = await _serve(
            service, run, ClientBase.wire(Decorations.fixed(Deps(breaker=breaker)), "rpc")
        )

    assert caught.value.code() == grpc.StatusCode.UNAVAILABLE
    assert service.calls == 0


@pytest.mark.asyncio
async def test_every_call_carries_the_trace_and_the_service_token() -> None:
    """Unary and streaming calls carry the active trace context and the bearer token.

    Why this test is important:
      - The server continues the caller's trace only if the context crosses the wire, and
        a server with service auth rejects a call without the token.

    What it tests:
      - Inside an active span, both calls carry a ``traceparent`` with the span's trace id
        and ``authorization: Bearer tok``; the stream yields its chunks.
    """
    service = _Service()
    tracer = TracerProvider().get_tracer("test")

    async def run(channel: grpc.aio.Channel) -> list[bytes]:
        """Make one unary and one streaming call.

        Args:
            channel: The client channel.

        Returns:
            The streamed chunks.

        """
        await channel.unary_unary("/test.Svc/Flaky")(b"hi")
        return [chunk async for chunk in channel.unary_stream("/test.Svc/Stream")(b"s")]

    with tracer.start_as_current_span("caller") as span:
        trace_id = format(span.get_span_context().trace_id, "032x")
        chunks = await _serve(
            service, run, ClientBase.wire(Decorations(), "rpc"), token=_FAKE_CRED_1
        )

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
    """The factory returns a decorated channel without I/O and fails loudly.

    On an unknown kind.

    Why this test is important:
      - The composition root builds the channel at boot with zero infrastructure; a typo
        in the kind must not silently pick a backend.

    What it tests:
      - The gRPC kind returns a ``grpc.aio.Channel``; an unknown kind raises
        ``AppValueError``.
    """
    config = RpcConfig(
        name="retrieval", service_token=_FAKE_CRED_2, channel=ChannelConfig("127.0.0.1:1")
    )
    channel = new_rpc_channel_from_config(config)
    assert isinstance(channel, grpc.aio.Channel)
    await channel.close()
    with pytest.raises(AppValueError, match="unknown rpc kind"):
        _ = new_rpc_channel_from_config(
            RpcConfig(kind=cast("RpcKind", cast("object", "kafka")))
        )
    assert RpcKind("grpc") is RpcKind.GRPC
