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
        """Return a no-op counter.

        Args:
            name: The metric name.
            help_text: The metric's description, exported as its help.
            labels: The label names its series carry; ``None`` for none.

        Returns:
            A no-op counter.

        """
        return _NullCounter()

    @override
    def histogram(
        self,
        name: str,
        help_text: str,
        labels: list[str] | None = None,
        buckets: list[float] | None = None,
    ) -> MetricHistogram:
        """Return a no-op histogram.

        Args:
            name: The metric name.
            help_text: The metric's description, exported as its help.
            labels: The label names its series carry; ``None`` for none.
            buckets: Upper bounds of the buckets; ``None`` uses the backend's defaults.

        Returns:
            A no-op histogram.

        """
        return _NullHistogram()

    @override
    def gauge(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricGauge:
        """Return a no-op gauge.

        Args:
            name: The metric name.
            help_text: The metric's description, exported as its help.
            labels: The label names its series carry; ``None`` for none.

        Returns:
            A no-op gauge.

        """
        return _NullGauge()


class _NullCounter(MetricCounter):
    """No-op counter."""

    @override
    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Discard the increment.

        Args:
            value: Ignored.
            **labels: The label values selecting the series.

        """


class _NullHistogram(MetricHistogram):
    """No-op histogram."""

    @override
    def observe(self, value: float, **labels: str) -> None:
        """Discard the observation.

        Args:
            value: The observed value.
            **labels: The label values selecting the series.

        """


class _NullGauge(MetricGauge):
    """No-op gauge."""

    @override
    def set(self, value: float, **labels: str) -> None:
        """Discard the set.

        Args:
            value: Ignored.
            **labels: The label values selecting the series.

        """

    @override
    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Discard the increment.

        Args:
            value: Ignored.
            **labels: The label values selecting the series.

        """

    @override
    def dec(self, value: float = 1.0, **labels: str) -> None:
        """Discard the decrement.

        Args:
            value: How much to subtract.
            **labels: The label values selecting the series.

        """
