"""Unit tests for the gRPC service health surface (``controllers.health``)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import aiohttp
import pytest
from aiohttp.test_utils import TestClient, TestServer

from techai_webutils.controllers.health import build_health_app, start_health_server


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


@pytest.mark.asyncio
async def test_start_health_server_serves_until_cleaned_up() -> None:
    """Test that the started server answers on its port with no probe configured.

    Why this test is important:
      - The service runtime only starts the runner and cleans it up on drain; a server that
        does not listen leaves every probe failing.

    What it tests:
      - ``start_health_server`` on an ephemeral port answers ``/readyz`` 200 (no drain flag and
        no probe means ready) until ``cleanup``.
    """
    runner = await start_health_server("svc", "127.0.0.1", 0)
    try:
        host, port = runner.addresses[0][:2]
        async with (
            aiohttp.ClientSession() as session,
            session.get(f"http://{host}:{port}/readyz") as response,
        ):
            status = response.status
    finally:
        await runner.cleanup()

    assert status == 200
