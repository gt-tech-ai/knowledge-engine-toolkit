"""Tests for the shared gRPC serve loop: start, readiness, drain on SIGTERM."""

from __future__ import annotations

import asyncio
import contextlib
import os
import signal
import socket
from typing import cast
from unittest.mock import AsyncMock, MagicMock, create_autospec

import grpc
import pytest
from aiohttp import ClientSession

from techai_webutils.controllers.grpc.serve import GrpcServing, serve_until_signalled

_GRACE_SECONDS = 3.0
"""The drain grace the test serve loop is configured with."""


def _free_port() -> int:
    """Return a TCP port nothing listens on.

    Returns:
        The port.

    """
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return cast("int", probe.getsockname()[1])


@pytest.mark.asyncio
async def test_serves_until_sigterm_and_sheds_readiness_before_the_drain() -> None:
    """The server runs until SIGTERM, and readiness turns 503 before the gRPC drain.

    Why this test is important:
      - Kubernetes must stop routing to a pod before its gRPC server rejects calls; if
        readiness stayed 200 into the drain, in-flight clients would see UNAVAILABLE
        during every rollout.

    What it tests:
      - the server is started on its port and the on-serving hook runs once it serves;
        /readyz is 200 while serving; after SIGTERM, /readyz is 503 by the time
        ``server.stop`` is called, with the configured grace; the health server closes
        when the exit stack unwinds.
    """
    health_port = _free_port()
    readyz = f"http://127.0.0.1:{health_port}/readyz"
    server = cast("MagicMock", create_autospec(grpc.aio.Server, instance=True))
    serving = asyncio.Event()
    statuses: list[int] = []

    async def status() -> int:
        """Return /readyz's status code.

        Returns:
            The status code.

        """
        async with ClientSession() as http, http.get(readyz) as response:
            return response.status

    async def on_stop(_grace: float) -> None:
        """Record readiness at the moment the drain starts.

        Args:
            _grace: Unused.

        """
        statuses.append(await status())

    server.stop = AsyncMock(side_effect=on_stop)

    async def on_serving() -> None:
        """Signal that the server is serving, once /readyz answers."""
        statuses.append(await status())
        serving.set()

    async with contextlib.AsyncExitStack() as stack:
        task = asyncio.create_task(
            serve_until_signalled(
                server,
                GrpcServing("probe", "127.0.0.1", 50051, health_port, _GRACE_SECONDS),
                stack,
                on_serving=on_serving,
            )
        )
        _ = await asyncio.wait_for(serving.wait(), timeout=5)
        os.kill(os.getpid(), signal.SIGTERM)
        await asyncio.wait_for(task, timeout=5)

    cast("MagicMock", server.add_insecure_port).assert_called_once_with("[::]:50051")
    cast("AsyncMock", server.start).assert_awaited_once()
    cast("AsyncMock", server.stop).assert_awaited_once_with(_GRACE_SECONDS)
    assert statuses == [200, 503]
