"""Prometheus metrics setup with FastAPI instrumentation.

Provides a configured Prometheus registry and common metrics
for HTTP request duration, count, and in-flight requests.

new_metrics() returns an interfaces.MetricsProvider for dependency injection.
create_metrics() returns pre-configured HTTP server metrics.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, cast

from opentelemetry import trace as _otel_trace
from prometheus_client import (
    REGISTRY,
    CollectorRegistry,
    Counter,
    Gauge,
    Histogram,
)

from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricGauge,
    MetricHistogram,
    MetricsProvider,
)

if TYPE_CHECKING:
    from collections.abc import Callable


def _trace_exemplar() -> dict[str, str] | None:
    """Return a ``{"trace_id": ...}`` exemplar for the active span, or None when untraced.

    Attaching the active trace id as a Prometheus exemplar is what lets a Grafana metric panel link a
    latency-spike bucket straight to the trace that produced it (the metric->trace jump wired in the
    Prometheus datasource's exemplarTraceIdDestinations). Done transparently here so EVERY histogram
    observation carries an exemplar when it runs inside a span — no caller or interface change. The
    32-hex trace id is well under Prometheus' 128-char exemplar-label budget.
    """
    ctx = _otel_trace.get_current_span().get_span_context()
    if ctx.is_valid:
        return {"trace_id": format(ctx.trace_id, "032x")}
    return None


class PrometheusMetricsProvider(MetricsProvider):
    """MetricsProvider backed by prometheus_client.

    Args:
        registry: Optional custom Prometheus registry. Uses the default if None.

    """

    def __init__(self, registry: CollectorRegistry | None = None) -> None:
        """Store the registry, defaulting to the global Prometheus REGISTRY."""
        self._registry = registry or REGISTRY

    def _get_or_create[T](
        self,
        name: str,
        expected_type: type,
        labelnames: list[str],
        create: Callable[[], T],
    ) -> T:
        """Create a metric, or reuse an already-registered one of the same name (idempotent).

        A second in-process init of the SAME metric — a supervised restart, a second observer, or
        two tests in one interpreter — reuses the existing series instead of raising 'Duplicated
        timeseries in CollectorRegistry'. The global REGISTRY default is kept, so the
        ``/metrics`` scrape is unaffected.

        Reuse is deliberately narrow: only a collector of the SAME prometheus type and label names is
        handed back. A same-name collision with a different type or label set is a real programming
        error prometheus_client raises on — that ValueError is re-raised rather than returning a
        mismatched collector that would misreport (or blow up) at record time.
        """
        try:
            return create()
        except ValueError:
            existing = self._registered_collector(name)
            if existing is None:
                raise
            if not isinstance(existing, expected_type) or (
                list(getattr(existing, "_labelnames", ())) != labelnames
            ):
                raise  # same name, different type/labels → surface the real conflict
            return cast("T", existing)

    def _registered_collector(self, name: str) -> object | None:
        """Return the collector already registered under ``name``, or None.

        Reaches into prometheus_client privates (``_names_to_collectors`` /
        ``_collector_to_names``) because there is no public lookup-by-name API: the public
        ``CollectorRegistry`` surface is register / unregister / collect / get_sample_value /
        target info, and the duplicate-registration ValueError names no collector. The read goes
        through ``vars()`` on purpose, as an explicit and visible dependency on those internals
        rather than a suppressed private-member access. These attribute names are an internal
        contract; a prometheus_client bump could rename them, which
        ``tests/unit/foundation_metrics_idempotent_test.py`` exercises against the pinned version.
        """
        internals = vars(self._registry)
        names_to_collectors = cast("dict[str, object]", internals["_names_to_collectors"])
        existing = names_to_collectors.get(name)
        if existing is not None:
            return existing
        for collector in cast("dict[object, object]", internals["_collector_to_names"]):
            if getattr(collector, "_name", None) == name:
                return collector
        return None

    def counter(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricCounter:
        """Create a Prometheus counter metric (idempotent on a repeated in-process init)."""
        c = self._get_or_create(
            name,
            Counter,
            labels or [],
            lambda: Counter(name, help_text, labels or [], registry=self._registry),
        )
        return _PrometheusCounter(c, labels or [])

    def histogram(
        self,
        name: str,
        help_text: str,
        labels: list[str] | None = None,
        buckets: list[float] | None = None,
    ) -> MetricHistogram:
        """Create a Prometheus histogram metric."""
        kwargs: dict[str, Any] = {"registry": self._registry}
        if buckets is not None:
            kwargs["buckets"] = buckets
        h = self._get_or_create(
            name,
            Histogram,
            labels or [],
            lambda: Histogram(name, help_text, labels or [], **kwargs),
        )
        return _PrometheusHistogram(h, labels or [])

    def gauge(
        self, name: str, help_text: str, labels: list[str] | None = None
    ) -> MetricGauge:
        """Create a Prometheus gauge metric (idempotent on a repeated in-process init)."""
        g = self._get_or_create(
            name,
            Gauge,
            labels or [],
            lambda: Gauge(name, help_text, labels or [], registry=self._registry),
        )
        return _PrometheusGauge(g, labels or [])


class _PrometheusCounter(MetricCounter):
    """Adapter wrapping a prometheus_client Counter behind MetricCounter."""

    def __init__(self, inner: Counter, label_names: list[str]) -> None:
        """Wrap the prometheus Counter and remember its label names."""
        self._inner = inner
        self._label_names = label_names

    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Increment the counter, selecting the labelled child when labels are given."""
        if labels:
            self._inner.labels(**labels).inc(value)
        else:
            self._inner.inc(value)


class _PrometheusHistogram(MetricHistogram):
    """Adapter wrapping a prometheus_client Histogram behind MetricHistogram."""

    def __init__(self, inner: Histogram, label_names: list[str]) -> None:
        """Wrap the prometheus Histogram and remember its label names."""
        self._inner = inner
        self._label_names = label_names

    def observe(self, value: float, **labels: str) -> None:
        """Record an observation, tagging it with the active trace as a Prometheus exemplar.

        The exemplar (``trace_id`` of the current span) is what powers Grafana's metric->trace jump.
        prometheus_client only accepts exemplars on an OpenMetrics-capable registry, so a
        ``ValueError``/``TypeError`` falls back to a plain observation rather than dropping the sample.
        """
        target = self._inner.labels(**labels) if labels else self._inner
        exemplar = _trace_exemplar()
        if exemplar is not None:
            try:
                target.observe(value, exemplar=exemplar)
                return
            except (ValueError, TypeError):
                pass  # registry without exemplar support -> record the value without the exemplar
        target.observe(value)


class _PrometheusGauge(MetricGauge):
    """Adapter wrapping a prometheus_client Gauge behind MetricGauge."""

    def __init__(self, inner: Gauge, label_names: list[str]) -> None:
        """Wrap the prometheus Gauge and remember its label names."""
        self._inner = inner
        self._label_names = label_names

    def set(self, value: float, **labels: str) -> None:
        """Set the gauge value, selecting the labelled child when labels are given."""
        if labels:
            self._inner.labels(**labels).set(value)
        else:
            self._inner.set(value)

    def inc(self, value: float = 1.0, **labels: str) -> None:
        """Increment the gauge, selecting the labelled child when labels are given."""
        if labels:
            self._inner.labels(**labels).inc(value)
        else:
            self._inner.inc(value)

    def dec(self, value: float = 1.0, **labels: str) -> None:
        """Decrement the gauge, selecting the labelled child when labels are given."""
        if labels:
            self._inner.labels(**labels).dec(value)
        else:
            self._inner.dec(value)


def new_metrics(registry: CollectorRegistry | None = None) -> MetricsProvider:
    """Create a MetricsProvider backed by Prometheus.

    This is the preferred factory for dependency injection. To swap the metrics
    backend, provide a different implementation of interfaces.MetricsProvider.

    Args:
        registry: Optional custom Prometheus registry. Uses the default if None.

    Returns:
        An interfaces.MetricsProvider implementation.

    """
    return PrometheusMetricsProvider(registry)


def create_metrics(
    service_name: str,
    registry: CollectorRegistry | None = None,
) -> ServiceMetrics:
    """Create standard service metrics.

    Args:
        service_name: Service name for metric labels.
        registry: Optional custom registry. Uses default if None.

    Returns:
        ServiceMetrics instance with pre-configured metrics.

    """
    reg = registry or REGISTRY
    return ServiceMetrics(service_name, reg)


class ServiceMetrics:
    """Standard service metrics (requests, latency, in-flight)."""

    def __init__(self, service_name: str, registry: CollectorRegistry) -> None:
        """Register the request-count, duration, and in-flight metrics for the service."""
        self.request_count = Counter(
            f"{service_name}_requests_total",
            "Total number of requests",
            ["method", "endpoint", "status"],
            registry=registry,
        )
        self.request_duration = Histogram(
            f"{service_name}_request_duration_seconds",
            "Request duration in seconds",
            ["method", "endpoint"],
            registry=registry,
        )
        self.in_flight = Gauge(
            f"{service_name}_in_flight_requests",
            "Number of in-flight requests",
            registry=registry,
        )
