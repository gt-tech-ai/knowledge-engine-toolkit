"""Render the default Prometheus registry in the format the scraper asks for.

The histograms attach the active trace id as an exemplar (``metrics._trace_exemplar``), but the
plain text format has no place for exemplars: only OpenMetrics carries them. A scraper that
offers ``application/openmetrics-text`` (Prometheus does by default) gets OpenMetrics, so the
exemplars reach Prometheus' exemplar storage and Grafana's metric-to-trace jump; any other
client gets the text format, unchanged.
"""

from __future__ import annotations

from prometheus_client import REGISTRY
from prometheus_client.exposition import choose_encoder


def render_metrics(accept: str) -> tuple[bytes, str]:
    """Return the default registry's exposition and its content type for an ``Accept`` header.

    Args:
        accept: The request's ``Accept`` header ("" when absent).

    Returns:
        The body and its ``Content-Type``: OpenMetrics (with exemplars) when ``accept`` names
        it, else the Prometheus text format (prometheus_client's own negotiation).

    """
    encoder, content_type = choose_encoder(accept)
    return encoder(REGISTRY), content_type
