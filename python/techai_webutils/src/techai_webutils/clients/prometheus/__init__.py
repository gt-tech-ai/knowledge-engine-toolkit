"""Metrics queriers (``PrometheusKind``: ``stub``, ``http``).

Their factory and their client-tier decorator.
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
