"""The serve loop of a ``grpc.aio`` server: serve, report ready, drain on signal.

A service runs this lifecycle around its own servicers: start the ``grpc.aio`` server on
its port, start the health app (``/healthz``, ``/readyz``, ``/metrics``;
``controllers.health``) on its own port, wait for SIGINT or SIGTERM, flip readiness to
503, then drain the gRPC server within its grace period. Readiness flips first so
Kubernetes stops routing to the pod before the server rejects new calls; liveness stays
200 throughout. The health app closes when the caller's exit stack unwinds, after the
drain. Unlike ``GracefulServer`` (gRPC health service only), this serves the HTTP probes
Kubernetes and a Prometheus scrape read.
"""

from __future__ import annotations

import asyncio
import contextlib
import signal
from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.controllers.health import start_health_server
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable
    from contextlib import AsyncExitStack

    import grpc

logger = get_logger(__name__)


@dataclass(frozen=True, slots=True)
class GrpcServing:
    """Where a gRPC server and its health app listen, and how long it drains."""

    name: str
    """The service's name: the health app's metric label and the log prefix."""
    host: str
    """The interface the health app binds."""
    port: int
    """The gRPC port (bound on every interface)."""
    health_port: int
    """The health app's port."""
    stop_grace_seconds: float
    """How long in-flight RPCs may run once the drain starts."""


async def serve_until_signalled(
    server: grpc.aio.Server,
    serving: GrpcServing,
    stack: AsyncExitStack,
    *,
    readiness_probe: Callable[[], Awaitable[list[str]]] | None = None,
    on_serving: Callable[[], Awaitable[object]] | None = None,
) -> None:
    """Serve ``server`` until SIGINT or SIGTERM, then shed readiness and drain it.

    Args:
        server: The configured server (servicers and interceptors registered, not
            started).
        serving: The ports, the health app's host and the drain grace.
        stack: The caller's exit stack; it closes the health app after the drain.
        readiness_probe: Reports unready dependencies to ``/readyz``; None checks none.
        on_serving: Awaited once the server and the health app are up (e.g. to start a
            consumer that must only run while serving).

    """
    _ = server.add_insecure_port(f"[::]:{serving.port}")
    await server.start()
    draining = asyncio.Event()
    runner = await start_health_server(
        serving.name,
        serving.host,
        serving.health_port,
        ready=lambda: not draining.is_set(),
        readiness_probe=readiness_probe,
    )
    _ = stack.push_async_callback(runner.cleanup)
    if on_serving is not None:
        _ = await on_serving()
    logger.info(
        "grpc service started",
        service=serving.name,
        grpc_port=serving.port,
        health_port=serving.health_port,
    )
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        with contextlib.suppress(NotImplementedError):
            loop.add_signal_handler(sig, stop.set)
    _ = await stop.wait()
    draining.set()
    logger.info("grpc service shutting down, draining", service=serving.name)
    await server.stop(serving.stop_grace_seconds)
