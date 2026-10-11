"""Tracer builder with kind enum, config dataclass, and factory function.

Creates tracer provider instances based on configuration. Supports OTEL
and NULL tracer kinds.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.tracer.null_tracer import NullTracerProvider
from techai_webutils.foundation.tracer.tracer import new_tracer

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.tracer import TracerProvider


class TracerKind(StrEnum):
    """Available tracer implementations."""

    OTEL = "otel"
    """OpenTelemetry provider exporting spans over OTLP to the collector."""
    NULL = "null"
    """No-op provider that discards spans (dev/test or tracing disabled)."""


@dataclass
class TracerConfig:
    """Configuration for tracer creation.

    Attributes:
        kind: Which tracer implementation to use.
        service_name: Name of the service for trace attribution.
        otlp_endpoint: OTLP collector gRPC endpoint.
        insecure: Whether to use insecure gRPC connection.

    """

    kind: TracerKind = TracerKind.OTEL
    """Which tracer implementation the factory builds (OTEL or NULL)."""
    service_name: str = "app"
    """Service name recorded as the span resource for trace attribution."""
    otlp_endpoint: str = "http://localhost:4317"
    """OTLP collector gRPC endpoint spans are exported to."""
    insecure: bool = True
    """Whether the OTLP gRPC connection skips TLS (true for local dev)."""


def default_config(service_name: str) -> TracerConfig:
    """Return a default tracer config (OTEL with given service name)."""
    return TracerConfig(kind=TracerKind.OTEL, service_name=service_name)


def new_tracer_from_config(config: TracerConfig) -> TracerProvider:
    """Create a tracer provider from config.

    Args:
        config: Tracer configuration specifying kind and parameters.

    Returns:
        A TracerProvider implementation matching the requested kind.

    Raises:
        ValueError: If the kind is unknown.

    """
    if config.kind == TracerKind.OTEL:
        return new_tracer(
            service_name=config.service_name,
            otlp_endpoint=config.otlp_endpoint,
            insecure=config.insecure,
        )

    if config.kind == TracerKind.NULL:
        return NullTracerProvider()

    msg = f"unknown tracer kind: {config.kind}"
    raise AppValueError(msg)
