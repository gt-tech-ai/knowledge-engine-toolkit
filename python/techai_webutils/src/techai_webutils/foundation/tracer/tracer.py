"""OpenTelemetry tracing setup with OTLP gRPC exporter.

Configures the OTel SDK TracerProvider with batch span processor
and OTLP gRPC exporter for sending traces to a collector.

new_tracer() returns an interfaces.TracerProvider for dependency injection.
configure_tracer() returns a raw OTel Tracer for stdlib use.
"""

from __future__ import annotations

from contextlib import contextmanager
from typing import TYPE_CHECKING

from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider as SDKTracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.sdk.trace.sampling import ParentBased, TraceIdRatioBased
from opentelemetry.trace import StatusCode

from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan

if TYPE_CHECKING:
    from collections.abc import Generator


class OTelTracerProvider(TracerProvider):
    """TracerProvider backed by OpenTelemetry.

    Args:
        inner: An OTel Tracer instance.
        provider: The SDK TracerProvider (for shutdown).

    """

    def __init__(self, inner: trace.Tracer, provider: SDKTracerProvider) -> None:
        """Store the OTel Tracer for span creation and the SDK provider for shutdown."""
        self._inner = inner
        self._provider = provider

    @contextmanager
    def span(
        self, name: str, **attributes: str | float | bool
    ) -> Generator[TracerSpan, None, None]:
        """Start a new trace span as a context manager."""
        with self._inner.start_as_current_span(name) as otel_span:
            wrapped = _OTelSpan(otel_span)
            for key, value in attributes.items():
                wrapped.set_attribute(key, value)
            yield wrapped

    def shutdown(self) -> None:
        """Shut down the underlying tracer provider and flush pending spans."""
        self._provider.shutdown()


class _OTelSpan(TracerSpan):
    """Adapter wrapping an OTel Span behind the TracerSpan interface."""

    def __init__(self, inner: trace.Span) -> None:
        """Wrap the underlying OTel span."""
        self._inner = inner

    def set_attribute(self, key: str, value: str | float | bool) -> None:  # noqa: FBT001
        """Attach a key/value attribute to the span."""
        self._inner.set_attribute(key, value)

    def record_error(self, error: BaseException) -> None:
        """Mark the span as errored and record the exception with its stack trace."""
        self._inner.set_status(StatusCode.ERROR, str(error))
        self._inner.record_exception(error)


def _setup_provider(
    service_name: str,
    otlp_endpoint: str,
    *,
    insecure: bool,
    sample_rate: float = 1.0,
) -> SDKTracerProvider:
    """Create and register an OTel TracerProvider with OTLP export.

    Args:
        service_name: Name of the service for trace attribution.
        otlp_endpoint: OTLP collector gRPC endpoint.
        insecure: Whether to use insecure gRPC connection.
        sample_rate: Fraction of root traces to sample (0.0-1.0); 1.0 keeps every
            trace. Set below 1.0 (via TRACE_SAMPLE_RATE) to cut export volume.

    Returns:
        The configured SDK TracerProvider.

    """
    resource = Resource.create({"service.name": service_name})
    # ParentBased preserves distributed traces (honour the caller's sampling
    # decision); TraceIdRatioBased samples root spans at sample_rate. Without this
    # the SDK samples 100% of spans, so the Python services exported ~10x what the
    # Go services do (which sample at TRACE_SAMPLE_RATE) and overran the OTLP export
    # deadline (StatusCode.DEADLINE_EXCEEDED against the collector).
    sampler = ParentBased(root=TraceIdRatioBased(sample_rate))
    provider = SDKTracerProvider(resource=resource, sampler=sampler)
    exporter = OTLPSpanExporter(endpoint=otlp_endpoint, insecure=insecure)
    provider.add_span_processor(BatchSpanProcessor(exporter))
    trace.set_tracer_provider(provider)
    return provider


def new_tracer(
    service_name: str,
    otlp_endpoint: str = "http://localhost:4317",
    *,
    insecure: bool = True,
    sample_rate: float = 1.0,
) -> TracerProvider:
    """Create a TracerProvider backed by OpenTelemetry.

    This is the preferred factory for dependency injection. To swap the tracing
    backend, provide a different implementation of interfaces.TracerProvider.

    Args:
        service_name: Name of the service for trace attribution.
        otlp_endpoint: OTLP collector gRPC endpoint.
        insecure: Whether to use insecure gRPC connection.
        sample_rate: Fraction of root traces to sample (0.0-1.0).

    Returns:
        An interfaces.TracerProvider implementation.

    """
    provider = _setup_provider(
        service_name, otlp_endpoint, insecure=insecure, sample_rate=sample_rate
    )
    return OTelTracerProvider(trace.get_tracer(service_name), provider)


def configure_tracer(
    service_name: str,
    otlp_endpoint: str = "http://localhost:4317",
    *,
    insecure: bool = True,
    sample_rate: float = 1.0,
) -> trace.Tracer:
    """Configure OpenTelemetry tracing with OTLP gRPC export.

    Args:
        service_name: Name of the service for trace attribution.
        otlp_endpoint: OTLP collector gRPC endpoint.
        insecure: Whether to use insecure gRPC connection.
        sample_rate: Fraction of root traces to sample (0.0-1.0).

    Returns:
        A configured Tracer instance.

    """
    _ = _setup_provider(
        service_name, otlp_endpoint, insecure=insecure, sample_rate=sample_rate
    )
    return trace.get_tracer(service_name)
