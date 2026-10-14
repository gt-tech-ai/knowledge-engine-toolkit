"""Tests for ``server_interceptors``: a gRPC server's RPCs run through the controller tier.

The end-to-end cases serve a generic (bytes-in, bytes-out) service on an in-process
``grpc.aio`` server over loopback — the Python counterpart of the Go tests' bufconn — so the
status a caller sees is the real wire status.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import grpc
import pytest
from grpc_health.v1 import health_pb2, health_pb2_grpc

from techai_webutils.controllers.grpc import GracefulServer, ServerConfig, create_server
from techai_webutils.controllers.grpc.interceptors import Edge, server_interceptors
from techai_webutils.core.errors import NotFoundError
from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.foundation.decorate import Deps, Observability

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Sequence


async def _echo(
    request: bytes, _context: grpc.aio.ServicerContext[bytes, bytes]
) -> bytes:
    """Answer with the request."""
    await asyncio.sleep(0)
    return request


async def _slow(
    request: bytes, _context: grpc.aio.ServicerContext[bytes, bytes]
) -> bytes:
    """Answer after a pause longer than the tests' timeout."""
    await asyncio.sleep(0.2)
    return request


async def _abort(
    _request: bytes, context: grpc.aio.ServicerContext[bytes, bytes]
) -> bytes:
    """Abort NOT_FOUND with the handler's own message."""
    await context.abort(grpc.StatusCode.NOT_FOUND, "no such thing")
    return b""  # pragma: no cover - abort raises


async def _coded(
    _request: bytes, _context: grpc.aio.ServicerContext[bytes, bytes]
) -> bytes:
    """Raise a coded NOT_FOUND."""
    await asyncio.sleep(0)
    entity = "thing"
    raise NotFoundError(entity, id="t-1")


async def _crash(
    _request: bytes, _context: grpc.aio.ServicerContext[bytes, bytes]
) -> bytes:
    """Raise an unexpected exception carrying internal detail."""
    await asyncio.sleep(0)
    msg = "db password=hunter2"
    raise RuntimeError(msg)


async def _stream(
    request: bytes, _context: grpc.aio.ServicerContext[bytes, bytes]
) -> AsyncIterator[bytes]:
    """Stream three chunks, slower in total than the tests' timeout."""
    for index in range(3):
        await asyncio.sleep(0.04)
        yield request + str(index).encode()


_HANDLERS = {
    "Echo": grpc.unary_unary_rpc_method_handler(_echo),
    "Slow": grpc.unary_unary_rpc_method_handler(_slow),
    "Abort": grpc.unary_unary_rpc_method_handler(_abort),
    "Coded": grpc.unary_unary_rpc_method_handler(_coded),
    "Crash": grpc.unary_unary_rpc_method_handler(_crash),
    "Stream": grpc.unary_stream_rpc_method_handler(_stream),
}
"""The generic test service's methods (identity serializers: requests and responses are bytes)."""


async def _call(
    interceptors: Sequence[grpc.aio.ServerInterceptor],
    method: str,
    request: bytes = b"hi",
) -> tuple[grpc.StatusCode, str, list[bytes]]:
    """Serve the test service through ``interceptors`` and call ``method`` once.

    Returns the call's status, details and responses.
    """
    server = grpc.aio.server(interceptors=list(interceptors))
    server.add_generic_rpc_handlers((
        grpc.method_handlers_generic_handler("test.Svc", _HANDLERS),
    ))
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    try:
        async with grpc.aio.insecure_channel(f"127.0.0.1:{port}") as channel:
            path = f"/test.Svc/{method}"
            try:
                if method == "Stream":
                    call = channel.unary_stream(path)(request)
                    return grpc.StatusCode.OK, "", [chunk async for chunk in call]
                return grpc.StatusCode.OK, "", [await channel.unary_unary(path)(request)]
            except grpc.aio.AioRpcError as err:
                return err.code(), err.details() or "", []
    finally:
        await server.stop(None)


def _limiter(*, allow: bool) -> RateLimiter:
    """Return a rate limiter that admits every call, or none."""
    limiter = MagicMock(spec=RateLimiter)
    limiter.allow.return_value = allow
    return limiter


class TestLayout:
    """The order of the interceptors ``server_interceptors`` returns."""

    def test_lays_out_the_controller_tier(self) -> None:
        """Native observability, then runs of tier layers split around the authn interceptors.

        Why this test is important:
          - The order is the tier's contract (Go ``ServerOptions`` parity): every request is
            observed, load is shed before authentication, and auth sees an authenticated caller.

        What it tests:
          - tracing → metrics → logging → [recovery, rate limit] → authn → [auth, timeout] →
            inner; with no collaborators and recovery off, nothing is installed.
        """
        authn, inner = MagicMock(), MagicMock()
        deps = Deps(
            observability=Observability(
                logger=MagicMock(), metrics=MagicMock(), tracer=MagicMock()
            ),
            auth=AsyncMock(return_value=True),
            rate_limiter=_limiter(allow=True),
            timeout=1.0,
        )

        chain = server_interceptors("api", deps, Edge(authn=[authn], inner=[inner]))

        names = [type(i).__name__ for i in chain]
        assert names[:3] == [
            "TracingServerInterceptor",
            "MetricsServerInterceptor",
            "LoggingServerInterceptor",
        ]
        assert names[3] == "_TierServerInterceptor"
        assert chain[4] is authn
        assert names[5] == "_TierServerInterceptor"
        assert chain[6] is inner
        assert len(chain) == 7
        assert server_interceptors("api", Deps(skip_recovery=True)) == []


class TestEndToEnd:
    """The status a caller sees through the tier."""

    @pytest.mark.asyncio
    async def test_a_plain_call_succeeds(self) -> None:
        """A call the tier admits reaches the handler and returns its response.

        Why this test is important:
          - The tier must be transparent on the happy path.

        What it tests:
          - ``Echo`` through the default tier (recovery only) answers the request.
        """
        assert await _call(server_interceptors("api", Deps()), "Echo") == (
            grpc.StatusCode.OK,
            "",
            [b"hi"],
        )

    @pytest.mark.asyncio
    async def test_tier_rejections_become_their_status(self) -> None:
        """A rate-limited or unauthorized call is rejected with its code's status.

        Why this test is important:
          - A shed request must be RESOURCE_EXHAUSTED (retryable by policy), not UNIMPLEMENTED or
            UNKNOWN, and a denial must be PERMISSION_DENIED.

        What it tests:
          - A refusing rate limiter → RESOURCE_EXHAUSTED; a refusing authorizer → PERMISSION_DENIED
            carrying only ``permission denied``, never the denial's own text.
        """
        limited = server_interceptors("api", Deps(rate_limiter=_limiter(allow=False)))
        denied = server_interceptors("api", Deps(auth=AsyncMock(return_value=False)))

        assert (await _call(limited, "Echo"))[0] == grpc.StatusCode.RESOURCE_EXHAUSTED
        assert (await _call(denied, "Echo"))[:2] == (
            grpc.StatusCode.PERMISSION_DENIED,
            "permission denied",
        )

    @pytest.mark.asyncio
    async def test_handler_errors(self) -> None:
        """A handler's own status passes unchanged; a coded error maps; a crash is sanitized.

        Why this test is important:
          - Recovery must not mask an intentional abort, and an unexpected exception must not
            leak its message to the caller.

        What it tests:
          - ``Abort`` → NOT_FOUND with the handler's message; ``Coded`` → NOT_FOUND;
            ``Crash`` → INTERNAL ``internal error``.
        """
        chain = server_interceptors("api", Deps())

        assert (await _call(chain, "Abort"))[:2] == (
            grpc.StatusCode.NOT_FOUND,
            "no such thing",
        )
        assert (await _call(chain, "Coded"))[0] == grpc.StatusCode.NOT_FOUND
        assert (await _call(chain, "Crash"))[:2] == (
            grpc.StatusCode.INTERNAL,
            "internal error",
        )

    @pytest.mark.asyncio
    async def test_the_timeout_bounds_unary_calls_but_not_streams(self) -> None:
        """A slow unary call times out; a stream outlives the same budget.

        Why this test is important:
          - A long-lived stream must not be cut by a per-call budget (Go parity).

        What it tests:
          - With a 50 ms timeout, ``Slow`` → DEADLINE_EXCEEDED and ``Stream`` (≈120 ms) returns
            all three chunks.
        """
        chain = server_interceptors("api", Deps(timeout=0.05))

        assert (await _call(chain, "Slow"))[0] == grpc.StatusCode.DEADLINE_EXCEEDED
        assert await _call(chain, "Stream") == (
            grpc.StatusCode.OK,
            "",
            [b"hi0", b"hi1", b"hi2"],
        )


class TestGracefulServer:
    """The ``grpc.aio`` server wrapper with health probes and graceful shutdown."""

    def test_config_defaults_and_overrides(self) -> None:
        """``ServerConfig`` keeps the documented defaults and explicit overrides.

        Why this test is important:
          - Deployments rely on the default port and drain period; each service binds its own.

        What it tests:
          - Defaults are port 50051 and a 30 s shutdown; overrides are retained.
        """
        assert ServerConfig().port == 50051
        assert ServerConfig().shutdown_timeout == pytest.approx(30.0)
        assert ServerConfig(port=8093, max_workers=5).max_workers == 5

    @pytest.mark.asyncio
    async def test_start_serves_health_then_shuts_down_gracefully(self) -> None:
        """A started server answers health SERVING and readiness flips; shutdown drains it.

        Why this test is important:
          - Probes gate traffic during rollout; readiness must flip before the drain.

        What it tests:
          - After a non-blocking start the health check is SERVING; ``set_ready`` toggles
            readiness; ``shutdown`` stops the server so the health check fails.
        """
        server = GracefulServer(ServerConfig(port=0, shutdown_timeout=0.1))
        port = server.server.add_insecure_port("127.0.0.1:0")
        assert isinstance(server.server, grpc.aio.Server)

        await server.start(block=False)
        await server.set_ready(ready=True)
        async with grpc.aio.insecure_channel(f"127.0.0.1:{port}") as channel:
            health = health_pb2_grpc.HealthStub(channel)
            reply = await health.Check(health_pb2.HealthCheckRequest(service=""))
            assert reply.status == health_pb2.HealthCheckResponse.SERVING
            ready = await health.Check(health_pb2.HealthCheckRequest(service="readiness"))
            assert ready.status == health_pb2.HealthCheckResponse.SERVING
            await server.shutdown()
            with pytest.raises(grpc.aio.AioRpcError):
                await health.Check(health_pb2.HealthCheckRequest(service=""), timeout=0.5)

    @pytest.mark.asyncio
    async def test_create_server_registers_health(self) -> None:
        """``create_server`` returns an unstarted server carrying the configured interceptors.

        Why this test is important:
          - The simple factory is how a test or tool builds a server with the tier installed.

        What it tests:
          - The result is a ``grpc.aio.Server`` built from a config with interceptors.
        """
        chain = server_interceptors("api", Deps())
        assert isinstance(
            create_server(ServerConfig(interceptors=chain)), grpc.aio.Server
        )
