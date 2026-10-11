"""Unit tests for NullMetricsProvider no-op implementation.

Tests that all null metric objects accept calls without raising exceptions.

# Running Tests

Run with: pytest tests/python/test_foundation/test_metrics_null.py -v
"""

from __future__ import annotations

from techai_webutils.foundation.metrics.null_metrics import NullMetricsProvider


class TestNullMetricsProvider:
    """Test suite for no-op metrics provider."""

    def test_counter_inc_is_noop(self) -> None:
        """Test that NullCounter.inc() does not raise.

        **Why this test is important:**
          - Metrics are optional in test environments
          - Counter calls must not crash when metrics are disabled

        **What it tests:**
          - inc() completes without error
        """
        provider = NullMetricsProvider()
        counter = provider.counter("test_counter", "A test counter")
        counter.inc()
        counter.inc(5.0)
        counter.inc(1.0, method="GET")

    def test_histogram_observe_is_noop(self) -> None:
        """Test that NullHistogram.observe() does not raise.

        **Why this test is important:**
          - Histogram observations must be silently discarded when disabled
          - Service code should not need to check if metrics are available

        **What it tests:**
          - observe() completes without error
        """
        provider = NullMetricsProvider()
        histogram = provider.histogram("test_histogram", "A test histogram")
        histogram.observe(0.5)
        histogram.observe(1.0, method="POST")

    def test_gauge_operations_are_noop(self) -> None:
        """Test that NullGauge.set/inc/dec() do not raise.

        **Why this test is important:**
          - Gauge operations (set, inc, dec) must all be safe to call
          - In-flight request tracking uses gauges; must not crash without Prometheus

        **What it tests:**
          - set(), inc(), and dec() all complete without error
        """
        provider = NullMetricsProvider()
        gauge = provider.gauge("test_gauge", "A test gauge")
        gauge.set(42.0)
        gauge.inc()
        gauge.inc(2.0)
        gauge.dec()
        gauge.dec(3.0)
        gauge.set(0.0, service="api")

    def test_counter_with_labels(self) -> None:
        """Test that NullCounter handles label arguments without error.

        **Why this test is important:**
          - Label-bearing metric calls are common in production
          - Null implementation must accept any label kwargs

        **What it tests:**
          - inc() with label kwargs does not raise
        """
        provider = NullMetricsProvider()
        counter = provider.counter("test_counter", "counter", labels=["method"])
        counter.inc(1.0, method="GET")
