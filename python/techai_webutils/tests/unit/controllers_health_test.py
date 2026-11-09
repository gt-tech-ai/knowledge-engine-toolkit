"""Tests for the shared health + metrics HTTP app.

The module under test is ``techai_webutils.controllers.health``.
"""

from __future__ import annotations

import asyncio
from http import HTTPStatus
from unittest.mock import AsyncMock, MagicMock

import pytest
from aiohttp.test_utils import TestClient, TestServer

from techai_webutils.controllers.health import (
    build_health_app,
    metrics_handler,
    start_health_server,
)


def test_metrics_handler_exposes_prometheus() -> None:
    """/metrics returns a 200 Prometheus exposition payload.

    Why this test is important:
      - The ServiceMonitor scrapes it; a wrong content type drops every series.

    What it tests:
      - a 200 with the Prometheus text content type
    """
    response = asyncio.run(metrics_handler(MagicMock()))
    assert response.status == HTTPStatus.OK
    assert response.headers["Content-Type"].startswith("text/plain")


def test_health_ok() -> None:
    """/healthz returns 200 ok.

    Why this test is important:
      - Liveness must stay 200 while the process lives, or K8s restarts a healthy pod.

    What it tests:
      - a 200 ``ok`` body
    """

    async def _run() -> tuple[int, str]:
        async with TestClient(TestServer(build_health_app("svc"))) as client:
            response = await client.get("/healthz")
            return response.status, await response.text()

    assert asyncio.run(_run()) == (HTTPStatus.OK, "ok")


def test_readyz_sheds_traffic_when_draining() -> None:
    """/readyz flips to 503 once draining so K8s stops routing before gRPC stop.

    /healthz (liveness) stays 200 throughout.

    Why this test is important:
      - Without shedding readiness first, a pod keeps receiving traffic through the 10s
        gRPC drain and clients routed in that window get UNAVAILABLE.

    What it tests:
      - /readyz is 200 while ready and 503 once the drain flag flips; /healthz (liveness)
        is 200 in both.
    """
    # The server's readiness flag: ready until the drain flips it.
    ready = MagicMock(return_value=True)

    async def _run() -> tuple[int, int, int]:
        app = build_health_app("svc", ready=ready)
        async with TestClient(TestServer(app)) as client:
            ready_code = (await client.get("/readyz")).status
            ready.return_value = False  # draining
            draining_ready = (await client.get("/readyz")).status
            draining_live = (await client.get("/healthz")).status
            return ready_code, draining_ready, draining_live

    ready_code, draining_ready, draining_live = asyncio.run(_run())
    assert ready_code == HTTPStatus.OK
    assert draining_ready == HTTPStatus.SERVICE_UNAVAILABLE
    assert draining_live == HTTPStatus.OK


def test_readyz_503_with_failed_list_when_dependency_unready() -> None:
    """/readyz returns 503 + a JSON failed list when a readiness probe reports a failure.

    Why this test is important:
      - A pod whose dependency is down (a knowledge base) must be pulled from
        Service endpoints (not restarted) so traffic isn't routed to a pod that will 5xx
        every query.

    What it tests:
      - A probe returning a non-empty failed list makes /readyz respond 503 with
        `{"failed": [...]}`.
    """

    async def _run() -> tuple[int, dict[str, list[str]]]:
        failing_probe = AsyncMock(return_value=["knowledge_base"])
        app = build_health_app("svc", readiness_probe=failing_probe)
        async with TestClient(TestServer(app)) as client:
            response = await client.get("/readyz")
            return response.status, await response.json()

    status, body = asyncio.run(_run())
    assert status == HTTPStatus.SERVICE_UNAVAILABLE
    assert body["failed"] == ["knowledge_base"]


def test_readyz_200_when_dependencies_ready() -> None:
    """/readyz returns 200 when the readiness probe reports no failed dependencies.

    Why this test is important:
      - The happy path must stay 200 or K8s never routes traffic to a healthy pod.

    What it tests:
      - A probe returning [] yields a 200 /readyz.
    """

    async def _run() -> int:
        ready_probe = AsyncMock(return_value=[])
        app = build_health_app("svc", readiness_probe=ready_probe)
        async with TestClient(TestServer(app)) as client:
            return (await client.get("/readyz")).status

    assert asyncio.run(_run()) == HTTPStatus.OK


def test_start_health_server_serves_on_the_given_port() -> None:
    """start_health_server binds the app on the given host and port, returning its runner.

    Why this test is important:
      - The probes reach the pod on the configured health port; a server that never
        binds fails every probe.

    What it tests:
      - a runner whose site answers /healthz on an ephemeral local port, then cleans up
    """

    async def _run() -> int:
        runner = await start_health_server("svc", "127.0.0.1", 0)
        try:
            return len(runner.addresses)
        finally:
            await runner.cleanup()

    assert asyncio.run(_run()) == 1


@pytest.mark.asyncio
async def test_readyz_follows_the_drain_flag_and_the_dependency_probe() -> None:
    """Test that readiness sheds traffic on drain and on an unready dependency, and liveness never does.

    Why this test is important:
      - A pod must leave the Service endpoints the moment its drain starts (before the gRPC
        server rejects calls) and while a dependency (a model still loading) is unready,
        without being restarted; liveness turning red would restart it instead.

    What it tests:
      - ``/readyz`` is 200 when ready, 503 ``{"failed": ["models"]}`` while the probe reports
        ``models``, and 503 ``{"failed": ["draining"]}`` once the drain flag flips; ``/healthz``
        is 200 throughout; ``/metrics`` serves the Prometheus text format, and OpenMetrics
        to a scraper whose ``Accept`` asks for it (the format that carries exemplars).
    """
    ready = MagicMock(return_value=True)
    probe = AsyncMock(return_value=[])
    app = build_health_app("svc", ready=ready, readiness_probe=probe)
    async with TestClient(TestServer(app)) as client:
        ready_status = (await client.get("/readyz")).status
        probe.return_value = ["models"]
        loading = await client.get("/readyz")
        loading_body = await loading.json()
        ready.return_value = False
        draining = await client.get("/readyz")
        draining_body = await draining.json()
        live = (await client.get("/healthz")).status
        metrics = await client.get("/metrics")
        openmetrics = await client.get(
            "/metrics", headers={"Accept": "application/openmetrics-text;version=1.0.0"}
        )

    assert ready_status == 200
    assert (loading.status, loading_body) == (503, {"failed": ["models"]})
    assert (draining.status, draining_body) == (503, {"failed": ["draining"]})
    assert live == 200
    assert metrics.status == 200
    assert metrics.headers["Content-Type"].startswith("text/plain")
    assert openmetrics.headers["Content-Type"].startswith("application/openmetrics-text")
