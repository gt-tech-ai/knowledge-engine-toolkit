"""Unit tests for the tracer builder (factory + config).

Tests that the builder correctly creates tracer instances based on TracerKind,
validates config, and provides sensible defaults.

# Running Tests

Run with: pytest tests/python/test_foundation/test_tracer_builder.py -v
"""

from __future__ import annotations

from unittest.mock import patch

import pytest

from techai_webutils.foundation.tracer.builder import (
    TracerConfig,
    TracerKind,
    default_config,
    new_tracer_from_config,
)
from techai_webutils.foundation.tracer.null_tracer import NullTracerProvider
from techai_webutils.foundation.tracer.tracer import OTelTracerProvider


class TestTracerBuilder:
    """Test suite for tracer builder factory."""

    def test_new_tracer_otel(self) -> None:
        """Test that TracerKind.OTEL creates an OTelTracerProvider.

        **Why this test is important:**
          - OTel is the production tracing backend
          - Builder must correctly route OTEL kind
          - Must mock OTel SDK to avoid real network connections

        **What it tests:**
          - Returned instance is an OTelTracerProvider
        """
        config = TracerConfig(kind=TracerKind.OTEL, service_name="test-svc")
        with (
            patch("techai_webutils.foundation.tracer.tracer._setup_provider"),
            patch("opentelemetry.trace.get_tracer"),
        ):
            tracer = new_tracer_from_config(config)
        assert isinstance(tracer, OTelTracerProvider)

    def test_new_tracer_null(self) -> None:
        """Test that TracerKind.NULL creates a NullTracerProvider.

        **Why this test is important:**
          - Null tracer disables tracing for tests and CLI tools
          - Builder must correctly route NULL kind

        **What it tests:**
          - Returned instance is a NullTracerProvider
        """
        config = TracerConfig(kind=TracerKind.NULL, service_name="test-svc")
        tracer = new_tracer_from_config(config)
        assert isinstance(tracer, NullTracerProvider)

    def test_unknown_kind_raises(self) -> None:
        """Test that an unknown kind raises ValueError.

        **Why this test is important:**
          - Config-driven values can contain typos
          - Fail-fast prevents silent misconfigurations

        **What it tests:**
          - ValueError raised for invalid kind
        """
        config = TracerConfig(kind="unknown", service_name="test-svc")  # type: ignore[arg-type]
        with pytest.raises(ValueError, match="unknown"):
            new_tracer_from_config(config)

    def test_default_config(self) -> None:
        """Test that default_config returns OTEL kind with service name.

        **Why this test is important:**
          - OTel is the standard tracing backend for production
          - Default must set the service name

        **What it tests:**
          - kind is TracerKind.OTEL
          - service_name matches the provided name
        """
        config = default_config("my-service")
        assert config.kind == TracerKind.OTEL
        assert config.service_name == "my-service"
