"""Metrics queriers, their factory and their client-tier decorator.

The kinds (``PrometheusKind``) are ``stub`` and ``http``.
"""

from techai_webutils.clients.prometheus.builder import (
    PrometheusConfig,
    PrometheusKind,
    new_metrics_querier_from_config,
)
from techai_webutils.clients.prometheus.decorated import decorate

__all__ = [
    "PrometheusConfig",
    "PrometheusKind",
    "decorate",
    "new_metrics_querier_from_config",
]
