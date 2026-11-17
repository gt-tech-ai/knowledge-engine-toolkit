"""Unit tests for the shared ``DEFAULT_DURATION_BUCKETS`` histogram boundaries."""

from __future__ import annotations

from prometheus_client import CollectorRegistry, Histogram

from techai_webutils.core.types.histogram_buckets import DEFAULT_DURATION_BUCKETS

_SLOW_PARSE_SECONDS = 45
"""A parse slower than 30 s but under a minute."""

_SLOW_ROUND_SECONDS = 250
"""An index round slower than 2 min but under 5."""


def test_default_duration_buckets_resolve_slow_operations() -> None:
    """Test that the shared duration buckets tell slow operations apart up to 5 min.

    **Why this test is important:**
      - Parses, model calls and KB rounds run for tens of seconds to minutes; with a
        10 s top bucket every one lands in +Inf, so a regression from 40 s to 4 min is
        invisible on a p95.

    **What it tests:**
      - a 45 s observation falls in the 60 s bucket and not the 30 s one
      - a 250 s observation falls in the 300 s bucket and not the 120 s one
      - the boundaries strictly increase, as Prometheus requires
    """
    histogram = Histogram(
        "op_duration_seconds",
        "operation duration",
        buckets=DEFAULT_DURATION_BUCKETS,
        registry=CollectorRegistry(),
    )
    histogram.observe(_SLOW_PARSE_SECONDS)
    histogram.observe(_SLOW_ROUND_SECONDS)

    counts = {
        sample.labels["le"]: sample.value
        for metric in histogram.collect()
        for sample in metric.samples
        if sample.name.endswith("_bucket")
    }

    assert list(DEFAULT_DURATION_BUCKETS) == sorted(set(DEFAULT_DURATION_BUCKETS))
    assert (counts["30.0"], counts["60.0"], counts["120.0"], counts["300.0"]) == (
        0,
        1,
        1,
        2,
    )
