"""PrometheusMetricsProvider creates metrics idempotently (get-or-create).

A second in-process init of the same metric name must reuse the existing collector instead of raising
prometheus_client's "Duplicated timeseries in CollectorRegistry" — so a supervised restart, a second
observer, or two tests in one interpreter no longer crash. The global ``REGISTRY`` default
is kept, so the ``/metrics`` scrape is unaffected.
"""

from __future__ import annotations

import pytest
from prometheus_client import CollectorRegistry

from techai_webutils.foundation.metrics.metrics import PrometheusMetricsProvider


class TestMetricsIdempotency:
    """PrometheusMetricsProvider is re-entrant: re-creating a metric reuses the existing series."""

    def test_reinit_reuses_series_instead_of_raising(self) -> None:
        """Test that re-creating same-named metrics on one registry does not raise Duplicated timeseries.

        **Why this test is important:**
          - Two providers on the same registry (a restart / second observer) previously raised
            'Duplicated timeseries', crashing startup; get-or-create makes the second reuse the series.

        **What it tests:**
          - A second provider creating counter/gauge/histogram of names the first already registered
            succeeds, and the returned metrics are usable.
        """
        reg = CollectorRegistry()
        first = PrometheusMetricsProvider(registry=reg)
        second = PrometheusMetricsProvider(registry=reg)

        first.counter("dup_widget", "help", labels=["k"])
        first.gauge("dup_depth", "help")
        first.histogram("dup_latency", "help")

        # Second init on the same registry must NOT raise.
        counter = second.counter("dup_widget", "help", labels=["k"])
        gauge = second.gauge("dup_depth", "help")
        histogram = second.histogram("dup_latency", "help")

        counter.inc(1.0, k="v")
        gauge.set(3.0)
        histogram.observe(0.1)

    def test_same_name_conflicting_metric_still_raises(self) -> None:
        """Test that a same-name metric with different labels or a different type is NOT reused.

        **Why this test is important:**
          - get-or-create reuse is only safe for a genuinely identical metric; silently returning a
            collector with a different label set — or a different type entirely — would misreport or
            blow up at record time. A real same-name conflict must still surface prometheus's error
            instead of being papered over.

        **What it tests:**
          - Re-creating a registered counter with different labels raises ValueError, and creating a
            gauge under a counter's name (type mismatch) also raises.
        """
        reg = CollectorRegistry()
        provider = PrometheusMetricsProvider(registry=reg)
        provider.counter("conflict_widget", "help", labels=["a"])

        with pytest.raises(ValueError, match=r"[Dd]uplicat"):
            provider.counter("conflict_widget", "help", labels=["b"])
        with pytest.raises(ValueError, match=r"[Dd]uplicat"):
            provider.gauge("conflict_widget", "help")
