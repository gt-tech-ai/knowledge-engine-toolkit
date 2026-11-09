"""Unit tests for ``render_metrics``.

The exposition format follows the scraper's ``Accept``.
"""

from __future__ import annotations

from opentelemetry.sdk.trace import TracerProvider

from techai_webutils.foundation.metrics import PrometheusMetricsProvider, render_metrics


def test_openmetrics_scrape_carries_the_trace_exemplar() -> None:
    """Test that an OpenMetrics scrape carries a traced observation's exemplar.

    And text does not.

    **Why this test is important:**
      - The histograms tag each observation with the active trace id, but only OpenMetrics
        can carry it; serving text to Prometheus drops every exemplar, so a latency spike
        can never be followed to its trace.

    **What it tests:**
      - An observation made inside a span, scraped with Prometheus' OpenMetrics
        ``Accept``, renders with the OpenMetrics content type and an exemplar naming that
        span's trace id; scraped with no ``Accept``, it renders as text with no exemplar.
    """
    histogram = PrometheusMetricsProvider().histogram(
        "exposition_test_seconds", "Exposition test.", ["stage"]
    )
    tracer = TracerProvider().get_tracer("exposition-test")
    with tracer.start_as_current_span("observe") as span:
        histogram.observe(0.2, stage="parse")
        trace_id = format(span.get_span_context().trace_id, "032x")

    body, content_type = render_metrics(
        "application/openmetrics-text;version=1.0.0,text/plain;version=0.0.4;q=0.5"
    )
    text, text_type = render_metrics("")

    assert content_type.startswith("application/openmetrics-text")
    assert f'# {{trace_id="{trace_id}"}}' in body.decode()
    assert text_type.startswith("text/plain")
    assert "exposition_test_seconds_bucket" in text.decode()
    assert trace_id not in text.decode()
