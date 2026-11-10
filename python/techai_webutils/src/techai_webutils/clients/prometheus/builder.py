"""``new_metrics_querier_from_config``: the config-selected ``MetricsQuerier``.

It mirrors Go's ``clients/prometheus``.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

import httpx

from techai_webutils.clients.prometheus.http.querier import HttpMetricsQuerier
from techai_webutils.clients.prometheus.stub.querier import StubMetricsQuerier
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.metrics_query import MetricsQuerier

DEFAULT_MAX_BODY_BYTES = 32 << 20
"""The response-body cap (32 MiB), as Go's ``DefaultMaxBodyBytes``."""


class PrometheusKind(StrEnum):
    """Which metrics querier to build."""

    STUB = "stub"
    """Answers zero with no I/O (the default)."""
    HTTP = "http"
    """Queries a Prometheus server's HTTP API."""


@dataclass(frozen=True, slots=True)
class PrometheusConfig:
    """Selects and configures the metrics querier."""

    kind: PrometheusKind = PrometheusKind.STUB
    """The querier to build; ``stub`` by default."""
    base_url: str = ""
    """The Prometheus server root (``http``; required there)."""
    timeout_seconds: float = 30.0
    """Bounds each request, body included."""
    max_body_bytes: int = DEFAULT_MAX_BODY_BYTES
    """A larger response is ``INVALID_INPUT``; 0 means no cap."""


def new_metrics_querier_from_config(
    config: PrometheusConfig, *, client: httpx.AsyncClient | None = None
) -> MetricsQuerier:
    """Build the ``MetricsQuerier`` ``config.kind`` selects.

    The ``http`` kind sends through ``client`` when given (tests, a shared pool), else
    through a new ``httpx.AsyncClient`` bounded by ``timeout_seconds``.

    Args:
        config: Selects the backend and carries its settings.
        client: A shared httpx client; the http kind opens its own when ``None``.

    Returns:
        The ``MetricsQuerier`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind, or ``http`` without a ``base_url``
            (``INVALID_INPUT``).

    """
    if config.kind == PrometheusKind.STUB:
        return StubMetricsQuerier()
    if config.kind == PrometheusKind.HTTP:
        if not config.base_url:
            msg = "prometheus: base_url is required for the http kind"
            raise AppValueError(msg)
        http = client or httpx.AsyncClient(timeout=config.timeout_seconds)
        return HttpMetricsQuerier(
            http, config.base_url, max_body_bytes=config.max_body_bytes
        )
    msg = f"unknown prometheus kind: {config.kind!r}"
    raise AppValueError(msg)
