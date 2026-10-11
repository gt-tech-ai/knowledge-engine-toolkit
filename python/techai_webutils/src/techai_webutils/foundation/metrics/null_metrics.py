"""No-op metrics implementation.

All metric operations are silent no-ops. Useful for tests and environments
where Prometheus is not available.
"""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricGauge,
    MetricHistogram,
    MetricsProvider,
)


class NullMetricsProvider(MetricsProvider):
    """No-op metrics provider that creates null metric objects."""

    @override
    def counter(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricCounter:
        """Return a no-op counter."""
        return _NullCounter()

    @override
    def histogram(
        self,
        name: str,
        help_text: str,
        labels: list[str] | None = None,
        buckets: list[float] | None = None,
    ) -> MetricHistogram:
        """Return a no-op histogram."""
        return _NullHistogram()

    @override
    def gauge(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricGauge:
        """Return a no-op gauge."""
        return _NullGauge()


class _NullCounter(MetricCounter):
    """No-op counter."""

    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Discard the increment."""


class _NullHistogram(MetricHistogram):
    """No-op histogram."""

    def observe(self, value: float, **labels: str) -> None:
        """Discard the observation."""


class _NullGauge(MetricGauge):
    """No-op gauge."""

    def set(self, value: float, **labels: str) -> None:
        """Discard the set."""

    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Discard the increment."""

    def dec(self, value: float = 1.0, **labels: str) -> None:
        """Discard the decrement."""
