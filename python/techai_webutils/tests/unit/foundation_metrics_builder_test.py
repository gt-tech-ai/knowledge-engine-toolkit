"""Unit tests for the metrics builder (factory + config).

Tests that the builder correctly creates metrics instances based on MetricsKind,
validates config, and provides sensible defaults.

# Running Tests

Run with: pytest tests/python/test_foundation/test_metrics_builder.py -v
"""

from __future__ import annotations

import pytest
from prometheus_client import CollectorRegistry

from techai_webutils.foundation.metrics.builder import (
    MetricsConfig,
    MetricsKind,
    default_config,
    new_metrics_from_config,
)
from techai_webutils.foundation.metrics.metrics import PrometheusMetricsProvider
from techai_webutils.foundation.metrics.null_metrics import NullMetricsProvider


class TestMetricsBuilder:
    """Test suite for metrics builder factory."""

    def test_new_metrics_prometheus(self) -> None:
        """Test that MetricsKind.PROMETHEUS creates a PrometheusMetricsProvider.

        **Why this test is important:**
          - Prometheus is the production metrics backend
          - Builder must correctly route PROMETHEUS kind

        **What it tests:**
          - Returned instance is a PrometheusMetricsProvider
        """
        registry = CollectorRegistry()
        config = MetricsConfig(kind=MetricsKind.PROMETHEUS, registry=registry)
        provider = new_metrics_from_config(config)
        assert isinstance(provider, PrometheusMetricsProvider)

    def test_new_metrics_null(self) -> None:
        """Test that MetricsKind.NULL creates a NullMetricsProvider.

        **Why this test is important:**
          - Null provider disables metrics collection entirely
          - Builder must correctly route NULL kind

        **What it tests:**
          - Returned instance is a NullMetricsProvider
        """
        config = MetricsConfig(kind=MetricsKind.NULL)
        provider = new_metrics_from_config(config)
        assert isinstance(provider, NullMetricsProvider)

    def test_unknown_kind_raises(self) -> None:
        """Test that an unknown kind raises ValueError.

        **Why this test is important:**
          - Config-driven values can contain typos
          - Fail-fast prevents silent misconfigurations

        **What it tests:**
          - ValueError raised for invalid kind
        """
        config = MetricsConfig(kind="unknown")  # type: ignore[arg-type]
        with pytest.raises(ValueError, match="unknown"):
            new_metrics_from_config(config)

    def test_default_config(self) -> None:
        """Test that default_config returns PROMETHEUS kind.

        **Why this test is important:**
          - Prometheus is the standard metrics backend for production
          - Default must enable metrics collection

        **What it tests:**
          - kind is MetricsKind.PROMETHEUS
        """
        config = default_config()
        assert config.kind == MetricsKind.PROMETHEUS


class TestPrometheusMetricsLabels:
    """Test suite for Prometheus metrics with labels and ServiceMetrics."""

    def test_counter_with_labels(self) -> None:
        """Test that PrometheusMetricsProvider counter works with labels.

        **Why this test is important:**
          - Labeled counters are used for per-method, per-endpoint request tracking
          - Labels must be correctly passed to the Prometheus client library
          - Incorrect label handling would cause metric registration errors in production

        **What it tests:**
          - counter().inc() with label kwargs does not raise
        """
        registry = CollectorRegistry()
        provider = PrometheusMetricsProvider(registry)
        counter = provider.counter(
            "test_counter_labels", "A test counter", labels=["method"]
        )
        counter.inc(1.0, method="GET")
        counter.inc(1.0, method="POST")

    def test_counter_without_labels(self) -> None:
        """Test that a label-free counter works with plain inc().

        **Why this test is important:**
          - Simple counters without labels are common for total request counts
          - The provider must support both labeled and unlabeled counters
          - Calling inc() without labels on an unlabeled counter must not error

        **What it tests:**
          - counter().inc() without labels does not raise
        """
        registry = CollectorRegistry()
        provider = PrometheusMetricsProvider(registry)
        counter = provider.counter("test_counter_nolabels", "A test counter")
        counter.inc()
        counter.inc(5.0)

    def test_histogram_with_labels(self) -> None:
        """Test that PrometheusMetricsProvider histogram works with labels.

        **Why this test is important:**
          - Labeled histograms track latency per-endpoint for SLA monitoring
          - Custom buckets are essential for matching application-specific latency profiles
          - Grafana dashboards use labeled histograms for percentile calculations

        **What it tests:**
          - histogram().observe() with label kwargs does not raise
          - Custom buckets are accepted
        """
        registry = CollectorRegistry()
        provider = PrometheusMetricsProvider(registry)
        histogram = provider.histogram(
            "test_histogram_labels",
            "A test histogram",
            labels=["endpoint"],
            buckets=[0.01, 0.05, 0.1, 0.5, 1.0],
        )
        histogram.observe(0.05, endpoint="/api/v1/items")
        histogram.observe(0.2, endpoint="/api/v1/users")

    def test_histogram_without_labels(self) -> None:
        """Test that a label-free histogram works with plain observe().

        **Why this test is important:**
          - Simple histograms are used for aggregate latency tracking
          - The provider must support both labeled and unlabeled histograms
          - observe() without labels on an unlabeled histogram must not error

        **What it tests:**
          - histogram().observe() without labels does not raise
        """
        registry = CollectorRegistry()
        provider = PrometheusMetricsProvider(registry)
        histogram = provider.histogram("test_histogram_nolabels", "A test histogram")
        histogram.observe(0.5)
        histogram.observe(1.0)

    def test_gauge_with_labels(self) -> None:
        """Test that PrometheusMetricsProvider gauge works with labels.

        **Why this test is important:**
          - Labeled gauges track per-service in-flight requests and resource usage
          - All three gauge operations (set, inc, dec) must work with labels
          - Incorrect label handling would corrupt per-service monitoring data

        **What it tests:**
          - gauge().set/inc/dec with label kwargs do not raise
        """
        registry = CollectorRegistry()
        provider = PrometheusMetricsProvider(registry)
        gauge = provider.gauge("test_gauge_labels", "A test gauge", labels=["service"])
        gauge.set(42.0, service="api")
        gauge.inc(1.0, service="api")
        gauge.dec(1.0, service="api")

    def test_gauge_without_labels(self) -> None:
        """Test that a label-free gauge works with plain set/inc/dec().

        **Why this test is important:**
          - Simple gauges are used for global metrics like total active connections
          - The provider must support unlabeled gauges for simple use cases
          - All three operations (set, inc, dec) must function without labels

        **What it tests:**
          - gauge().set/inc/dec without labels do not raise
        """
        registry = CollectorRegistry()
        provider = PrometheusMetricsProvider(registry)
        gauge = provider.gauge("test_gauge_nolabels", "A test gauge")
        gauge.set(10.0)
        gauge.inc()
        gauge.dec()

    def test_service_metrics_instantiation(self) -> None:
        """Test that ServiceMetrics creates request_count, request_duration, and in_flight.

        **Why this test is important:**
          - ServiceMetrics is the standard metrics bundle used by all services
          - The three core metrics (count, duration, in-flight) are required for RED monitoring
          - Missing any metric would create blind spots in Grafana dashboards

        **What it tests:**
          - create_metrics() returns a ServiceMetrics instance
          - ServiceMetrics.request_count, request_duration, in_flight are accessible
          - Metric operations do not raise
        """
        from techai_webutils.foundation.metrics.metrics import create_metrics

        registry = CollectorRegistry()
        sm = create_metrics("test_svc", registry=registry)
        assert sm.request_count is not None
        assert sm.request_duration is not None
        assert sm.in_flight is not None
        # Exercise the metrics
        sm.request_count.labels(method="GET", endpoint="/health", status="200").inc()
        sm.request_duration.labels(method="GET", endpoint="/health").observe(0.05)
        sm.in_flight.inc()
        sm.in_flight.dec()

    def test_new_metrics_factory(self) -> None:
        """Test that new_metrics returns a usable MetricsProvider.

        **Why this test is important:**
          - new_metrics() is the convenience factory for quick provider creation
          - Custom registries are needed for test isolation (avoid global state pollution)
          - The returned provider must support all metric types for full observability

        **What it tests:**
          - new_metrics() with a custom registry returns a PrometheusMetricsProvider
          - The returned provider can create counters, histograms, and gauges
        """
        from techai_webutils.foundation.metrics.metrics import new_metrics

        registry = CollectorRegistry()
        provider = new_metrics(registry)
        assert isinstance(provider, PrometheusMetricsProvider)
        counter = provider.counter("nm_counter", "test")
        counter.inc()
        histogram = provider.histogram("nm_histogram", "test")
        histogram.observe(0.5)
        gauge = provider.gauge("nm_gauge", "test")
        gauge.set(1.0)
