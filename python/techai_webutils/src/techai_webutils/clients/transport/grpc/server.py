"""Async gRPC server factory with health service, graceful shutdown, and interceptor chain.

Creates a configured ``grpc.aio`` server with:
- 16MB max message size (send and receive)
- Health service auto-registered (``/healthz`` equivalent)
- Readiness probe support (toggled via ``set_ready()``)
- Graceful shutdown with a configurable drain period
- Support for the async interceptor chain

Mirrors Go's ``go/clients/transport/connect/server.go`` ``Server`` struct.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import signal
from dataclasses import dataclass, field
from typing import Any

import grpc
from grpc_health.v1 import health_pb2, health_pb2_grpc
from grpc_health.v1.health import aio as health_aio

_16MB = 16 * 1024 * 1024
"""Default gRPC max send/receive message size, in bytes (16 MiB)."""
_logger = logging.getLogger(__name__)


@dataclass
class ServerConfig:
    """gRPC server configuration."""

    port: int = 50051
    """TCP port the gRPC server listens on."""
    max_send_message_length: int = _16MB
    """Maximum outbound gRPC message size, in bytes."""
    max_receive_message_length: int = _16MB
    """Maximum inbound gRPC message size, in bytes."""
    max_concurrent_rpcs: int | None = None
    """Cap on concurrently handled RPCs; None leaves it unbounded."""
    max_workers: int = 10
    """Retained for config compat; grpc.aio uses the event loop, not a thread pool."""
    shutdown_timeout: float = 30.0
    """Grace period, in seconds, to drain in-flight RPCs during shutdown."""
    interceptors: list[Any] = field(default_factory=list)
    """Server interceptors applied to every RPC (logging, metrics, auth, ...)."""


def _options(cfg: ServerConfig) -> list[tuple[str, Any]]:
    """Build the gRPC channel-arg options (message-size limits) from the config."""
    return [
        ("grpc.max_send_message_length", cfg.max_send_message_length),
        ("grpc.max_receive_message_length", cfg.max_receive_message_length),
    ]


class GracefulServer:
    """Async gRPC server wrapper with graceful shutdown and health probes.

    Matches Go's ``Server`` struct with ``start()`` and ``shutdown()`` methods (async here).

    Example::

        cfg = ServerConfig(port=8093, shutdown_timeout=15.0)
        server = GracefulServer(cfg)
        server.register(add_MyServicer_to_server, my_servicer)
        await server.start()  # blocks until signal
    """

    def __init__(self, config: ServerConfig | None = None) -> None:
        """Initialize the server with config, an async health servicer, and a stop event.

        Args:
            config: Server configuration. Uses ``ServerConfig`` defaults if None.

        """
        self._cfg = config or ServerConfig()
        self._health_servicer = health_aio.HealthServicer()
        self._server = self._create_server()
        self._stop_event = asyncio.Event()

    def _create_server(self) -> grpc.aio.Server:
        """Build the async gRPC server with message-size options + the health service registered.

        The default service is marked SERVING in ``start()`` (the aio health ``set`` is a coroutine).
        """
        server = grpc.aio.server(
            interceptors=self._cfg.interceptors or None,
            options=_options(self._cfg),
            maximum_concurrent_rpcs=self._cfg.max_concurrent_rpcs,
        )
        health_pb2_grpc.add_HealthServicer_to_server(self._health_servicer, server)
        return server

    @property
    def server(self) -> grpc.aio.Server:
        """Access the underlying async gRPC server for service registration."""
        return self._server

    def enable_reflection(self, service_names: tuple[str, ...]) -> None:
        """Enable gRPC server reflection for developer tooling.

        Args:
            service_names: Fully qualified service names (e.g. ``demo.v1.UserService``).

        """
        from grpc_reflection.v1alpha import reflection as grpc_reflection  # noqa: PLC0415

        all_names = (*service_names, grpc_reflection.SERVICE_NAME)
        grpc_reflection.enable_server_reflection(all_names, self._server)
        _logger.info("gRPC reflection enabled for %d services", len(service_names))

    async def set_ready(self, *, ready: bool = True) -> None:
        """Toggle readiness probe status (matches Go's ``/readyz`` behavior)."""
        status = (
            health_pb2.HealthCheckResponse.SERVING
            if ready
            else health_pb2.HealthCheckResponse.NOT_SERVING
        )
        await self._health_servicer.set("readiness", status)

    async def start(self, *, block: bool = True) -> None:
        """Start the async gRPC server.

        Args:
            block: If True, installs signal handlers and blocks until a termination
                signal is received, then performs graceful shutdown.

        """
        await self._health_servicer.set("", health_pb2.HealthCheckResponse.SERVING)
        self._server.add_insecure_port(f"[::]:{self._cfg.port}")
        await self._server.start()
        _logger.info("gRPC server started on port %d", self._cfg.port)

        if block:
            self._install_signal_handlers()
            await self._stop_event.wait()
            await self.shutdown()

    async def shutdown(self) -> None:
        """Perform graceful shutdown: mark not-serving, drain existing connections, then stop."""
        _logger.info(
            "Initiating graceful shutdown (timeout=%.1fs)", self._cfg.shutdown_timeout
        )
        await self._health_servicer.set("", health_pb2.HealthCheckResponse.NOT_SERVING)
        await self.set_ready(ready=False)
        await self._server.stop(self._cfg.shutdown_timeout)
        _logger.info("gRPC server stopped")

    def _install_signal_handlers(self) -> None:
        """Install SIGTERM/SIGINT handlers that trip the stop event (no-op where unsupported)."""
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            with contextlib.suppress(NotImplementedError):
                loop.add_signal_handler(sig, self._stop_event.set)


def create_server(config: ServerConfig | None = None) -> grpc.aio.Server:
    """Create a configured async gRPC server with the health service registered.

    For simple use cases without graceful shutdown. For production use, prefer ``GracefulServer``.
    The caller starts the server and sets the SERVING health status (the aio health ``set`` is async).

    Args:
        config: Server configuration. Uses defaults if None.

    Returns:
        Configured but not-yet-started async gRPC server.

    """
    cfg = config or ServerConfig()
    server = grpc.aio.server(
        interceptors=cfg.interceptors or None,
        options=_options(cfg),
        maximum_concurrent_rpcs=cfg.max_concurrent_rpcs,
    )
    health_pb2_grpc.add_HealthServicer_to_server(health_aio.HealthServicer(), server)
    return server
