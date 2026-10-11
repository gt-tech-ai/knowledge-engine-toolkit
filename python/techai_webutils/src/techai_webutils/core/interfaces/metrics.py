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
        """Create or retrieve a counter metric."""
        ...

    @abstractmethod
    def histogram(
        self,
        name: str,
        help_text: str,
        labels: list[str] | None = None,
        buckets: list[float] | None = None,
    ) -> MetricHistogram:
        """Create or retrieve a histogram metric."""
        ...

    @abstractmethod
    def gauge(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricGauge:
        """Create or retrieve a gauge metric."""
        ...


class MetricCounter(ABC):
    """Abstract counter metric."""

    @abstractmethod
    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Increment the counter."""
        ...

    def increment(self, value: float = 1.0, **labels: str) -> None:
        """Alias for :meth:`inc` for readability."""
        self.inc(value, **labels)


class MetricHistogram(ABC):
    """Abstract histogram metric."""

    @abstractmethod
    def observe(self, value: float, **labels: str) -> None:
        """Record an observation."""
        ...


class MetricGauge(ABC):
    """Abstract gauge metric."""

    @abstractmethod
    def set(self, value: float, **labels: str) -> None:
        """Set the gauge to a value."""
        ...

    @abstractmethod
    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Increment the gauge."""
        ...

    @abstractmethod
    def dec(self, value: float = 1.0, **labels: str) -> None:
        """Decrement the gauge."""
        ...
