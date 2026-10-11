"""Metrics builder with kind enum, config dataclass, and factory function.

Creates metrics provider instances based on configuration. Supports PROMETHEUS
and NULL metrics kinds.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.metrics.metrics import PrometheusMetricsProvider
from techai_webutils.foundation.metrics.null_metrics import NullMetricsProvider

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.metrics import MetricsProvider


class MetricsKind(StrEnum):
    """Available metrics implementations."""

    PROMETHEUS = "prometheus"
    """Prometheus-backed provider exposing counters/histograms for scraping."""
    NULL = "null"
    """No-op provider that discards all metric writes (dev/test)."""


@dataclass
class MetricsConfig:
    """Configuration for metrics creation.

    Attributes:
        kind: Which metrics implementation to use.
        registry: Optional Prometheus CollectorRegistry (only used when kind is PROMETHEUS).

    """

    kind: MetricsKind = MetricsKind.PROMETHEUS
    """Which metrics implementation the factory builds (PROMETHEUS or NULL)."""
    registry: Any = None
    """Optional Prometheus ``CollectorRegistry`` (PROMETHEUS only; default registry when None)."""


def default_config() -> MetricsConfig:
    """Return a default metrics config (PROMETHEUS)."""
    return MetricsConfig(kind=MetricsKind.PROMETHEUS)


def new_metrics_from_config(config: MetricsConfig) -> MetricsProvider:
    """Create a metrics provider from config.

    Args:
        config: Metrics configuration specifying kind and parameters.

    Returns:
        A MetricsProvider implementation matching the requested kind.

    Raises:
        ValueError: If the kind is unknown.

    """
    if config.kind == MetricsKind.PROMETHEUS:
        return PrometheusMetricsProvider(registry=config.registry)

    if config.kind == MetricsKind.NULL:
        return NullMetricsProvider()

    msg = f"unknown metrics kind: {config.kind}"
    raise AppValueError(msg)
