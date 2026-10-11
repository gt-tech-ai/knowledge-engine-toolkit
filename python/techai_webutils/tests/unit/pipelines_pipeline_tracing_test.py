"""Tests for the AsyncPipelineBuilder tracing decorator (full-stack pipeline observability)."""

from __future__ import annotations

import asyncio
from unittest.mock import MagicMock, create_autospec

import pytest

from techai_webutils.core.errors.errors import InvalidInputError
from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan
from techai_webutils.pipelines.base import BaseAsyncPipeline
from techai_webutils.pipelines.decorators import AsyncPipelineBuilder


def _tracer() -> tuple[TracerProvider, TracerSpan]:
    """Generated-mock tracer whose span() yields a mock TracerSpan."""
    tracer = create_autospec(TracerProvider, instance=True)
    span = create_autospec(TracerSpan, instance=True)
    cm = MagicMock()
    cm.__enter__.return_value = span
    cm.__exit__.return_value = False
    tracer.span.return_value = cm
    return tracer, span


class TestAsyncPipelineTracing:
    """Tests for the async pipeline tracing."""

    @pytest.mark.asyncio
    async def test_spans_execution(self) -> None:
        """Test that with_tracing wraps execution in a span.

        **Why this test is important:**
          - Pipelines are a layer that must carry the full observability stack; without a span per
            execute, a slow transform is invisible in the trace.

        **What it tests:**
          - build().with_tracing(tracer) opens a span on execute and returns the result.
        """

        async def transform(x: str) -> str:
            await asyncio.sleep(0)
            return x.upper()

        tracer, _span = _tracer()
        p = (
            AsyncPipelineBuilder(BaseAsyncPipeline(transform), "meta")
            .with_tracing(tracer)
            .build()
        )

        assert await p.execute("hi") == "HI"
        tracer.span.assert_called_once()

    @pytest.mark.asyncio
    async def test_records_error_on_span(self) -> None:
        """Test that a failing execution records the error on the span.

        **Why this test is important:**
          - A span that doesn't capture the failure is worse than none — the trace would show the
            pipeline ran without surfacing that it errored, hiding the fault from observability.

        **What it tests:**
          - A transform raising an error records it on the span and re-raises.
        """

        async def failing(_x: str) -> str:
            await asyncio.sleep(0)
            msg = "bad"
            raise InvalidInputError(msg)

        tracer, span = _tracer()
        p = (
            AsyncPipelineBuilder(BaseAsyncPipeline(failing), "meta")
            .with_tracing(tracer)
            .build()
        )

        with pytest.raises(InvalidInputError):
            await p.execute("x")
        span.record_error.assert_called_once()
