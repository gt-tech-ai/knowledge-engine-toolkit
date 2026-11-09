"""Tests for the resilient Ray runtime's health signals.

Connect failures and the head probe.
"""

from __future__ import annotations

import asyncio
import socket
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock, create_autospec, patch

import grpc
import pytest
import pytest_asyncio

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.execution import StepResult
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
from techai_webutils.execution.executor.factory import (
    ExecutorConfig,
    ray_runtime_from_config,
)
from techai_webutils.execution.executor.ray_runtime import RayRuntime, RayUnavailableError
from techai_webutils.execution.executor.resilient_ray_runtime import ResilientRayRuntime

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_MODULE = "techai_webutils.execution.executor.resilient_ray_runtime"


class _GrpcFailure(grpc.RpcError):
    """A gRPC error carrying a status code.

    As the Ray Client raises on a failed connect.
    """

    def __init__(self, code: grpc.StatusCode) -> None:
        super().__init__(code.name)
        self._code = code

    def code(self) -> grpc.StatusCode:
        """Return the status code.

        Returns:
            The status code.

        """
        return self._code


def _inner() -> MagicMock:
    """Return a generated-mock inner ``RayRuntime``.

    Returns:
        A generated-mock inner ``RayRuntime``.

    """
    runtime = create_autospec(RayRuntime, instance=True)
    runtime.warm_up = AsyncMock()
    runtime.submit = AsyncMock(return_value=StepResult(name="ok"))
    return runtime


def _metrics() -> tuple[MetricsProvider, MagicMock]:
    """Return a mocked metrics provider and the counter it hands out.

    Returns:
        A mocked metrics provider and the counter it hands out.

    """
    counter = create_autospec(MetricCounter, instance=True)
    metrics = create_autospec(MetricsProvider, instance=True)
    metrics.counter.return_value = counter
    return metrics, counter


async def _mapper(item: int) -> StepResult:
    """Map nothing; the inner runtime is mocked.

    Returns:
        A passing step for the item.

    """
    await asyncio.sleep(0)
    return StepResult(name=str(item))


class TestConnectFailures:
    """``consecutive_connect_failures`` and ``ray_connect_failures_total``."""

    @pytest.mark.asyncio
    async def test_failures_are_counted_and_a_success_resets_them(self) -> None:
        """Three failed connects report 3, by gRPC status.

        A successful connect resets the count.

        Why this test is important:
          - A runtime whose client can no longer reconnect looks idle, not broken; the
            consumer alerts and fails liveness on this count, so it must rise per failure
            and clear on recovery.

        What it tests:
          - Three NOT_FOUND connect failures give ``consecutive_connect_failures() == 3``
            and three ``ray_connect_failures_total{reason="NOT_FOUND"}`` increments; the
            next successful connect gives 0.
        """
        inner = _inner()
        inner.warm_up.side_effect = [
            *[_GrpcFailure(grpc.StatusCode.NOT_FOUND)] * 3,
            None,
        ]
        metrics, counter = _metrics()
        runtime = ResilientRayRuntime(
            inner, max_attempts=3, base_delay=0, max_delay=0, metrics=metrics
        )

        with pytest.raises(RayUnavailableError):
            await runtime.warm_up()
        failures = runtime.consecutive_connect_failures()
        await runtime.warm_up()

        assert failures == 3
        assert runtime.consecutive_connect_failures() == 0
        metrics.counter.assert_called_once_with(
            "ray_connect_failures_total",
            "Failed Ray Client connect attempts, by gRPC status.",
            ["reason"],
        )
        assert counter.inc.call_count == 3
        counter.inc.assert_called_with(1.0, reason="NOT_FOUND")

    @pytest.mark.asyncio
    async def test_a_failure_without_a_grpc_status_is_counted_as_unknown(self) -> None:
        """A connect failure that carries no gRPC status is counted under ``UNKNOWN``.

        Why this test is important:
          - Ray raises plain ``ConnectionError``/``RuntimeError`` on some connect paths;
            those must still be counted, under a bounded label value.

        What it tests:
          - A ``ConnectionError`` connect failure increments the counter with
            ``reason="UNKNOWN"``.
        """
        inner = _inner()
        inner.warm_up.side_effect = ConnectionError("refused")
        metrics, counter = _metrics()
        runtime = ResilientRayRuntime(
            inner, max_attempts=1, base_delay=0, max_delay=0, metrics=metrics
        )

        with pytest.raises(RayUnavailableError):
            await runtime.warm_up()

        counter.inc.assert_called_once_with(1.0, reason="UNKNOWN")

    @pytest.mark.asyncio
    async def test_the_connect_wrapper_is_built_once_per_runtime(self) -> None:
        """The retry wrapper around the connect is built once.

        However many reconnects there are.

        Why this test is important:
          - Building the tenacity wrapper on every reconnect is wasted work on the hot
            path of a batch whose connection keeps dropping.

        What it tests:
          - Across 100 submissions that each lose the connection (forcing a reconnect
            before the next), the retry decorator is applied exactly once.
        """
        inner = _inner()
        inner.submit.side_effect = RayUnavailableError("lost")
        applications = MagicMock(side_effect=lambda fn: fn)
        with patch(f"{_MODULE}.retry_transient_async", return_value=applications):
            runtime = ResilientRayRuntime(inner)
        for item in range(100):
            with pytest.raises(RayUnavailableError):
                await runtime.submit(_mapper, item)

        assert applications.call_count == 1
        assert inner.warm_up.await_count == 100


@pytest_asyncio.fixture
async def open_port() -> AsyncIterator[int]:
    """Serve a TCP listener on a loopback port for the test, yielding the port.

    Yields:
        The port.

    """
    server = await asyncio.start_server(lambda _r, w: w.close(), "127.0.0.1", 0)
    port = server.sockets[0].getsockname()[1]
    try:
        yield port
    finally:
        server.close()
        await server.wait_closed()


def _closed_port() -> int:
    """Return a loopback port nothing listens on.

    Returns:
        A loopback port nothing listens on.

    """
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class TestHeadReachable:
    """``head_reachable``: whether the head is up while the client is failing."""

    @pytest.mark.asyncio
    async def test_reports_an_open_head_port_while_the_client_fails(
        self, open_port: int
    ) -> None:
        """The probe reports the head reachable even while every client connect fails.

        Why this test is important:
          - "Client stuck, head up" needs a restart of the client process; "head down"
            needs the cluster fixed. The consumer can only tell them apart with this
            probe.

        What it tests:
          - With every inner connect failing, ``head_reachable()`` is True for a listening
            port.
        """
        inner = _inner()
        inner.warm_up.side_effect = ConnectionError("stuck")
        runtime = ResilientRayRuntime(
            inner,
            max_attempts=1,
            base_delay=0,
            max_delay=0,
            head_probe_address=f"127.0.0.1:{open_port}",
        )
        with pytest.raises(RayUnavailableError):
            await runtime.warm_up()

        assert await runtime.head_reachable() is True

    @pytest.mark.asyncio
    async def test_reports_a_closed_head_port_as_unreachable(self) -> None:
        """The probe reports a head that refuses the connection as unreachable.

        Why this test is important:
          - A probe that returned True for a dead head would hide a cluster outage.

        What it tests:
          - ``head_reachable()`` is False for a loopback port nothing listens on.
        """
        runtime = ResilientRayRuntime(
            _inner(), head_probe_address=f"127.0.0.1:{_closed_port()}"
        )

        assert await runtime.head_reachable() is False

    @pytest.mark.asyncio
    async def test_without_an_address_the_probe_is_refused(self) -> None:
        """A probe with no configured address fails loudly instead of guessing.

        Why this test is important:
          - A default address would probe the wrong host and report a healthy head that is
            down.

        What it tests:
          - ``head_reachable()`` with no ``head_probe_address`` raises ``AppValueError``.
        """
        with pytest.raises(AppValueError, match="head_probe_address"):
            await ResilientRayRuntime(_inner()).head_reachable()

    @pytest.mark.asyncio
    async def test_the_factory_threads_the_probe_address(self) -> None:
        """``ExecutorConfig.ray_head_probe_address`` reaches the runtime the factory.

        Builds.

        Why this test is important:
          - The consumer configures the probe once in its config; a factory that dropped
            it would leave the probe refusing in production.

        What it tests:
          - ``ray_runtime_from_config`` with a probe address of a closed loopback port
            builds a runtime whose ``head_reachable()`` is False rather than refusing.
        """
        runtime = ray_runtime_from_config(
            ExecutorConfig(
                kind="ray", ray_head_probe_address=f"127.0.0.1:{_closed_port()}"
            )
        )

        assert await runtime.head_reachable() is False
