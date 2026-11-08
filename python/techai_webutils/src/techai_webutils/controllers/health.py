"""HTTP health and metrics surface of a gRPC service: ``/healthz``, ``/readyz`` and ``/metrics``.

A gRPC service still needs HTTP for its Kubernetes and Compose probes and its Prometheus scrape,
so it serves this small aiohttp app on its own port beside the gRPC one:

- ``/healthz`` (liveness) is 200 while the process runs, through a graceful drain too;
- ``/readyz`` (readiness) is 503 with ``{"failed": ["draining"]}`` once ``ready()`` turns false
  (flip it before stopping the gRPC server, so traffic drains away first), 503 with the failed
  dependencies' names while ``readiness_probe()`` returns any (e.g. models still loading), and
  200 otherwise;
- ``/metrics`` is the default Prometheus registry: OpenMetrics, with the histograms' trace
  exemplars, when the scraper asks for it (Prometheus does), else the text exposition format.

Every request runs through the aiohttp observability middleware under the service's name.
``controllers`` does not import this module, so aiohttp (installed with ``aiobotocore``) loads
only in a service that serves it.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from aiohttp import web

from techai_webutils.foundation.metrics.exposition import render_metrics
from techai_webutils.foundation.middleware.http_observability import (
    aiohttp_observability_middleware,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

type ReadinessProbe = Callable[[], Awaitable[list[str]]]
"""Returns the names of the dependencies that are not ready (empty when all are)."""


class HealthRoutes:
    """The probe and metrics handlers over a drain flag and a dependency probe."""

    def __init__(
        self,
        ready: Callable[[], bool] | None = None,
        readiness_probe: ReadinessProbe | None = None,
    ) -> None:
        """Gate ``/readyz`` on ``ready`` (always ready when None) and ``readiness_probe`` (none)."""
        self._ready = ready
        self._probe = readiness_probe

    @staticmethod
    async def healthz(_request: web.Request) -> web.Response:
        """Liveness: 200 while the process runs.

        Returns:
            A 200 ``ok`` response.

        """
        return web.Response(text="ok")

    async def readyz(self, _request: web.Request) -> web.Response:
        """Readiness: 503 while draining or while a dependency is not ready, else 200.

        Returns:
            200 ``ok``, or 503 with a JSON ``{"failed": [...]}`` body.

        """
        if self._ready is not None and not self._ready():
            return web.json_response({"failed": ["draining"]}, status=503)
        failed = await self._probe() if self._probe is not None else []
        if failed:
            return web.json_response({"failed": failed}, status=503)
        return web.Response(text="ok")

    @staticmethod
    async def metrics(request: web.Request) -> web.Response:
        """Prometheus metrics, rendered off the event loop.

        Args:
            request: The scrape; its ``Accept`` header picks the format.

        Returns:
            The default registry in OpenMetrics (with trace exemplars) when the scraper asks
            for it, else in the text exposition format.

        """
        body, content_type = await asyncio.to_thread(
            render_metrics, request.headers.get("Accept", "")
        )
        return web.Response(body=body, headers={"Content-Type": content_type})


def build_health_app(
    service: str,
    *,
    ready: Callable[[], bool] | None = None,
    readiness_probe: ReadinessProbe | None = None,
) -> web.Application:
    """Build the health app of ``service`` (see the module docstring for the routes).

    Args:
        service: The service name the observability middleware records requests under.
        ready: The drain flag; False once draining. None is always ready.
        readiness_probe: Returns the dependencies that are not ready. None checks none.

    Returns:
        The aiohttp application.

    """
    routes = HealthRoutes(ready, readiness_probe)
    app = web.Application(middlewares=[aiohttp_observability_middleware(service)])
    app.router.add_get("/healthz", routes.healthz)
    app.router.add_get("/readyz", routes.readyz)
    app.router.add_get("/metrics", routes.metrics)
    return app


async def start_health_server(
    service: str,
    host: str,
    port: int,
    *,
    ready: Callable[[], bool] | None = None,
    readiness_probe: ReadinessProbe | None = None,
) -> web.AppRunner:
    """Serve ``service``'s health app on ``host:port`` and return its runner (clean it up to stop).

    Args:
        service: The service name the observability middleware records requests under.
        host: The interface to listen on.
        port: The port to listen on.
        ready: The drain flag; False once draining. None is always ready.
        readiness_probe: Returns the dependencies that are not ready. None checks none.

    Returns:
        The started runner.

    """
    app = build_health_app(service, ready=ready, readiness_probe=readiness_probe)
    runner = web.AppRunner(app, access_log=None)
    await runner.setup()
    await web.TCPSite(runner, host, port).start()
    return runner
