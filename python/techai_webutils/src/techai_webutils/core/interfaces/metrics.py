"""Metrics interface for application-level metric recording.

Defines the abstract contract for metrics implementations.
Foundation implementations satisfy this interface; consumers depend only on it.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class MetricsProvider(ABC):
    """Abstract metrics provider for counters, histograms, and gauges.

    Implementations: Prometheus (default).
    All service code depends on this interface, never on a concrete metrics library.
    """

    @abstractmethod
    def counter(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricCounter:
        """Create or retrieve a counter metric.

        Args:
            name: The metric name.
            help_text: The metric's description, exported as its help.
            labels: The label names its series carry; ``None`` for none.

        Returns:
            The counter registered under ``name``; asking twice returns the same one.

        """
        ...

    @abstractmethod
    def histogram(
        self,
        name: str,
        help_text: str,
        labels: list[str] | None = None,
        buckets: list[float] | None = None,
    ) -> MetricHistogram:
        """Create or retrieve a histogram metric.

        Args:
            name: The metric name.
            help_text: The metric's description, exported as its help.
            labels: The label names its series carry; ``None`` for none.
            buckets: Upper bounds of the buckets; ``None`` uses the backend's defaults.

        Returns:
            The histogram registered under ``name``; asking twice returns the same one.

        """
        ...

    @abstractmethod
    def gauge(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricGauge:
        """Create or retrieve a gauge metric.

        Args:
            name: The metric name.
            help_text: The metric's description, exported as its help.
            labels: The label names its series carry; ``None`` for none.

        Returns:
            The gauge registered under ``name``; asking twice returns the same one.

        """
        ...


class MetricCounter(ABC):
    """Abstract counter metric."""

    @abstractmethod
    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Increment the counter.

        Args:
            value: How much to add; never negative.
            **labels: The label values selecting the series.

        """
        ...

    def increment(self, value: float = 1.0, **labels: str) -> None:
        """Alias for :meth:`inc` for readability.

        Args:
            value: How much to add; never negative.
            **labels: The label values selecting the series.

        """
        self.inc(value, **labels)


class MetricHistogram(ABC):
    """Abstract histogram metric."""

    @abstractmethod
    def observe(self, value: float, **labels: str) -> None:
        """Record an observation.

        Args:
            value: The observed value.
            **labels: The label values selecting the series.

        """
        ...


class MetricGauge(ABC):
    """Abstract gauge metric."""

    @abstractmethod
    def set(self, value: float, **labels: str) -> None:
        """Set the gauge to a value.

        Args:
            value: The gauge's new value.
            **labels: The label values selecting the series.

        """
        ...

    @abstractmethod
    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Increment the gauge.

        Args:
            value: How much to add.
            **labels: The label values selecting the series.

        """
        ...

    @abstractmethod
    def dec(self, value: float = 1.0, **labels: str) -> None:
        """Decrement the gauge.

        Args:
            value: How much to subtract.
            **labels: The label values selecting the series.

        """
        ...
