"""One-call observability bootstrap shared by all Python services.

setup_observability() wires structured JSON logging (with OTel trace_id/span_id
correlation and structured tracebacks) and OpenTelemetry tracing (OTLP gRPC
export to a collector). Combine with the HTTP middleware in
foundation.middleware.http_observability and the /metrics endpoint for full
metrics + logs + traces coverage.
"""

from __future__ import annotations

from techai_webutils.foundation.logger.logger import configure_logging
from techai_webutils.foundation.tracer.tracer import configure_tracer


def parse_sample_rate(raw: str | None) -> float:
    """Parse a TRACE_SAMPLE_RATE string into a 0.0-1.0 fraction, defaulting to 1.0.

    Invalid or out-of-range values fall back to 1.0 (sample everything) so a bad
    config never silently disables tracing. This is pure normalization logic (no env
    read) exposed so the composition-root entrypoints — the sanctioned place to read
    the environment (ARCHITECTURE.md#configuration) — can parse the raw TRACE_SAMPLE_RATE they read.
    """
    if not raw:
        return 1.0
    try:
        rate = float(raw)
    except ValueError:
        return 1.0
    return min(max(rate, 0.0), 1.0)


def setup_observability(
    service_name: str,
    *,
    level: str = "INFO",
    otlp_endpoint: str = "localhost:4317",
    trace_sample_rate: float = 1.0,
    redact_pii: bool = False,
) -> None:
    """Initialize logging + tracing for a service.

    The OTLP endpoint and trace sample rate are INJECTED by the caller (the
    composition-root entrypoint reads OTEL_EXPORTER_OTLP_ENDPOINT / TRACE_SAMPLE_RATE
    from the environment once and passes them down) — this foundation function does
    not read the environment itself (ARCHITECTURE.md#configuration).

    Args:
        service_name: Service name used for trace resource attribution + logs.
        level: Log level.
        otlp_endpoint: OTLP collector endpoint (host:port or URL); a URL scheme is
            stripped so an "http://host:4317"-style value also works.
        trace_sample_rate: Span sampling fraction 0.0-1.0 (parse_sample_rate above
            normalizes a raw string into this).
        redact_pii: Redact PII in every log line (pass the ``logging_redact_pii`` setting).

    """
    _ = configure_logging(level=level, redact_pii=redact_pii)

    endpoint = otlp_endpoint
    # The gRPC exporter wants a bare host:port; strip any URL scheme so the
    # OTEL_EXPORTER_OTLP_ENDPOINT convention ("http://host:4317") also works.
    if "://" in endpoint:
        endpoint = endpoint.split("://", 1)[1].rstrip("/")
    # configure_tracer installs the global tracer provider; spans created by the
    # HTTP/gRPC middleware are exported to the OTLP collector and correlated into logs.
    _ = configure_tracer(
        service_name, otlp_endpoint=endpoint, insecure=True, sample_rate=trace_sample_rate
    )
