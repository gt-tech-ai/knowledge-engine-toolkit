"""Health + metrics HTTP app for a gRPC server (its own port beside the gRPC one).

Liveness/readiness (``/healthz``, ``/readyz``) and Prometheus metrics (``/metrics``),
served by a small aiohttp app alongside the gRPC server so container and Kubernetes probes
have an HTTP surface. Each service injects its own drain flag and dependency-readiness
probe and names itself for the request metrics.
``controllers.grpc.serve.serve_until_signalled`` runs it beside a ``grpc.aio`` server and
flips its drain flag on shutdown.
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


async def metrics_handler(request: web.Request) -> web.Response:
    """Prometheus metrics endpoint (scraped by the ServiceMonitor).

    The exposition is rendered on a worker thread, so a large registry never blocks the
    event loop serving gRPC.

    Args:
        request: The scrape; its ``Accept`` header picks the format.

    Returns: The default registry in OpenMetrics (with trace exemplars) when the scraper
        asks for it, else in the text exposition format.

    """
    body, content_type = await asyncio.to_thread(
        render_metrics, request.headers.get("Accept", "")
    )
    return web.Response(body=body, headers={"Content-Type": content_type})


def _probe(
    ready: Callable[[], bool],
    readiness_probe: Callable[[], Awaitable[list[str]]] | None,
) -> Callable[[web.Request], Awaitable[web.Response]]:
    """Return a probe handler: 503 when not ready or a dependency fails, else 200.

    Liveness is the same probe with no conditions: always ready and no dependencies, so
    it answers 200 for as long as the process serves requests, through a drain too.

    Args:
        ready: The drain flag; False once draining.
        readiness_probe: Returns the unready dependencies; None checks none.

    Returns:
        The handler.

    """

    async def probe(_request: web.Request) -> web.Response:
        """Answer the probe; a 503 body is the JSON failed list.

        Args:
            _request: The incoming request (unused).

        Returns:
            200 when ready, else 503 with the failed list.

        """
        if not ready():
            return web.json_response({"failed": ["draining"]}, status=503)
        failed = [] if readiness_probe is None else await readiness_probe()
        if failed:
            return web.json_response({"failed": failed}, status=503)
        return web.Response(text="ok", status=200)

    return probe


def _always_ready() -> bool:
    """Default readiness probe: always ready (used when no drain signal is wired).

    Returns:
        Always True.

    """
    return True


def build_health_app(
    service: str,
    ready: Callable[[], bool] = _always_ready,
    readiness_probe: Callable[[], Awaitable[list[str]]] | None = None,
) -> web.Application:
    """Build the aiohttp health app: ``/healthz``, ``/readyz`` and ``/metrics``.

    ``/healthz`` is liveness, ``/readyz`` readiness, ``/metrics`` the Prometheus scrape.

    ``ready`` gates ``/readyz`` on the drain flag (503 once draining) so K8s stops routing
    the moment a drain begins, before the gRPC server rejects new calls.
    ``readiness_probe`` additionally checks the external dependencies (e.g. a knowledge
    base's connectivity) and, when any fail, returns 503 with a JSON ``{"failed":
    [...]}`` body so the pod is removed from Service endpoints without a restart.
    ``/healthz`` (liveness) stays 200 throughout.

    Args:
        service: The service name the request metrics are labelled with.
        ready: The drain flag; False once draining.
        readiness_probe: Returns the unready dependencies; None checks none.

    Returns:
        The health app.

    """
    app = web.Application(middlewares=[aiohttp_observability_middleware(service)])
    _ = app.router.add_get("/healthz", _probe(_always_ready, None))
    _ = app.router.add_get("/readyz", _probe(ready, readiness_probe))
    _ = app.router.add_get("/metrics", metrics_handler)
    return app


async def start_health_server(
    service: str,
    host: str,
    port: int,
    *,
    ready: Callable[[], bool] = _always_ready,
    readiness_probe: Callable[[], Awaitable[list[str]]] | None = None,
) -> web.AppRunner:
    """Start the aiohttp health/metrics server and return its runner.

    The app is the one ``build_health_app`` builds.

    Args:
        service: The service name the request metrics are labelled with.
        host: The interface to listen on.
        port: The port to listen on.
        ready: The drain flag; False once draining.
        readiness_probe: Returns the unready dependencies; None checks none.

    Returns:
        The started runner.

    """
    runner = web.AppRunner(
        build_health_app(service, ready, readiness_probe), access_log=None
    )
    await runner.setup()
    await web.TCPSite(runner, host, port).start()
    return runner
