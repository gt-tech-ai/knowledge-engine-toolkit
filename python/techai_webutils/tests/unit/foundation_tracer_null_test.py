"""Unit tests for NullTracerProvider no-op implementation.

Tests that the null tracer's span context manager works correctly and all
span operations are silent no-ops.

# Running Tests

Run with: pytest tests/python/test_foundation/test_tracer_null.py -v
"""

from __future__ import annotations

from techai_webutils.foundation.tracer.null_tracer import NullTracerProvider


class TestNullTracerProvider:
    """Test suite for no-op tracer provider."""

    def test_span_yields_null_span(self) -> None:
        """Test that span() yields a span object as a context manager.

        **Why this test is important:**
          - The tracer contract requires span() to work as a context manager
          - Service code uses `with tracer.span("op") as span:` pattern

        **What it tests:**
          - span context manager yields a non-None span object
        """
        tracer = NullTracerProvider()
        with tracer.span("test-op") as span:
            assert span is not None

    def test_span_set_attribute_is_noop(self) -> None:
        """Test that span.set_attribute() does not raise.

        **Why this test is important:**
          - Service code annotates spans with attributes for observability
          - Must not crash when tracing is disabled

        **What it tests:**
          - set_attribute() completes without error
        """
        tracer = NullTracerProvider()
        with tracer.span("test-op") as span:
            span.set_attribute("key", "value")
            span.set_attribute("count", 42)

    def test_span_record_error_is_noop(self) -> None:
        """Test that span.record_error() does not raise.

        **Why this test is important:**
          - Error recording on spans must be safe when tracing is disabled
          - Exception handling code should not itself throw

        **What it tests:**
          - record_error() completes without error
        """
        tracer = NullTracerProvider()
        with tracer.span("test-op") as span:
            span.record_error(ValueError("test error"))

    def test_span_with_attributes(self) -> None:
        """Test that span() accepts initial attributes without error.

        **Why this test is important:**
          - The TracerProvider.span() interface accepts **attributes
          - Null implementation must accept and ignore them

        **What it tests:**
          - span() with keyword attributes does not raise
        """
        tracer = NullTracerProvider()
        with tracer.span("test-op", key="value", count=5) as span:
            assert span is not None

    def test_shutdown_is_noop(self) -> None:
        """Test that shutdown() does not raise.

        **Why this test is important:**
          - Service shutdown calls tracer.shutdown() for flush
          - Must be safe when tracing is disabled

        **What it tests:**
          - shutdown() completes without error
        """
        tracer = NullTracerProvider()
        tracer.shutdown()
