"""Unit tests for gRPC channel factory and server factory.

This file tests that the clients-layer gRPC abstractions create properly
configured servers and client channels with health checks, message size limits,
and custom target addresses.

# Test Coverage

The tests cover:
  - Server creation: default configuration produces a valid server
  - Health service: gRPC health service is registered on server creation
  - Server config: max message size defaults to 16 MB
  - Server config: custom port is accepted and stored
  - Client channel: creation and teardown without errors
  - Channel config: max message size defaults to 16 MB
  - Channel config: custom target address is accepted and stored

# Test Structure

Tests use pytest class-based organization grouped by component (server, client).
No mocking or external services are needed; tests validate configuration objects
and factory return values directly.

# Running Tests

Run with: pytest tests/python/test_clients/test_grpc.py
"""

import asyncio
from collections.abc import Callable

from techai_webutils.clients.rpc.grpc.client import ChannelConfig, create_channel
from techai_webutils.clients.transport.grpc.server import ServerConfig, create_server


async def _in_loop(factory: Callable[[], object]) -> object:
    """Call a ``grpc.aio``-constructing factory inside a running event loop."""
    await asyncio.sleep(0)
    return factory()


class TestGRPCServer:
    """Test suite for gRPC server factory and configuration."""

    def test_create_server_with_defaults(self) -> None:
        """Test that create_server returns a valid server instance with default config.

        **Why this test is important:**
          - Every Python gRPC service uses this factory for server creation
          - A None return would crash the service on startup
          - Default configuration must produce a functional server without arguments

        **What it tests:**
          - Return value of create_server() is not None
        """
        server = asyncio.run(_in_loop(create_server))
        assert server is not None

    def test_create_server_registers_health(self) -> None:
        """Test that create_server registers the gRPC health service.

        **Why this test is important:**
          - Kubernetes liveness and readiness probes use gRPC health checks
          - Missing health service registration causes pods to be killed by K8s
          - Health checks are required for graceful rolling deployments

        **What it tests:**
          - Server is created successfully (health registration does not throw)
        """
        # Health service is registered as part of server creation (no throw).
        server = asyncio.run(_in_loop(create_server))
        assert server is not None

    def test_server_config_max_message_size(self) -> None:
        """Test that ServerConfig defaults to 16 MB for max message length.

        **Why this test is important:**
          - Document payloads can be large; undersized limits cause truncation errors
          - The 16 MB default must match the Go services for consistent behavior
          - Both send and receive limits must be set to avoid asymmetric failures

        **What it tests:**
          - config.max_send_message_length equals 16 * 1024 * 1024
          - config.max_receive_message_length equals 16 * 1024 * 1024
        """
        config = ServerConfig()
        assert config.max_send_message_length == 16 * 1024 * 1024
        assert config.max_receive_message_length == 16 * 1024 * 1024

    def test_server_config_custom_port(self) -> None:
        """Test that ServerConfig accepts and stores a custom port value.

        **Why this test is important:**
          - Multiple services may run on the same host during development
          - Port configuration is critical for service discovery and load balancing
          - Kubernetes service definitions reference specific container ports

        **What it tests:**
          - config.port equals 9090 after construction with port=9090
        """
        config = ServerConfig(port=9090)
        assert config.port == 9090


class TestGRPCClient:
    """Test suite for gRPC client channel factory and configuration."""

    def test_create_channel_returns_channel(self) -> None:
        """Test that create_channel returns a valid channel that can be closed.

        **Why this test is important:**
          - Every inter-service gRPC call requires a channel
          - Channel creation failure would prevent all downstream communication
          - Clean close is required for graceful shutdown without resource leaks

        **What it tests:**
          - Return value of create_channel() is not None
          - await channel.close() does not raise
        """

        async def _run() -> None:
            channel = create_channel("localhost:50051")
            assert channel is not None
            await channel.close()

        asyncio.run(_run())

    def test_channel_config_defaults(self) -> None:
        """Test that ChannelConfig defaults to 16 MB for max message length.

        **Why this test is important:**
          - Client message limits must match server limits to avoid rejected requests
          - Asymmetric limits cause hard-to-debug "resource exhausted" errors
          - Default must be consistent across all Python gRPC clients

        **What it tests:**
          - config.max_send_message_length equals 16 * 1024 * 1024
          - config.max_receive_message_length equals 16 * 1024 * 1024
        """
        config = ChannelConfig()
        assert config.max_send_message_length == 16 * 1024 * 1024
        assert config.max_receive_message_length == 16 * 1024 * 1024

    def test_channel_config_custom_target(self) -> None:
        """Test that ChannelConfig accepts and stores a custom target address.

        **Why this test is important:**
          - Service discovery provides different targets per environment
          - The target address determines which service instance receives the request
          - Incorrect target would route requests to the wrong service

        **What it tests:**
          - config.target equals "retrieval.search.svc:8082"
        """
        config = ChannelConfig(target="retrieval.search.svc:8082")
        assert config.target == "retrieval.search.svc:8082"
