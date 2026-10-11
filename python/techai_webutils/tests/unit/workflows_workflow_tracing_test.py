"""Tests for the AsyncWorkflowBuilder tracing decorator (full-stack workflow observability)."""

from __future__ import annotations

import asyncio
from unittest.mock import MagicMock, create_autospec

import pytest

from techai_webutils.core.errors.errors import InvalidInputError
from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan
from techai_webutils.workflows.base import BaseAsyncWorkflow
from techai_webutils.workflows.decorators import AsyncWorkflowBuilder


def _tracer() -> tuple[TracerProvider, TracerSpan]:
    """Generated-mock tracer whose span() yields a mock TracerSpan."""
    tracer = create_autospec(TracerProvider, instance=True)
    span = create_autospec(TracerSpan, instance=True)
    cm = MagicMock()
    cm.__enter__.return_value = span
    cm.__exit__.return_value = False
    tracer.span.return_value = cm
    return tracer, span


class TestAsyncWorkflowTracing:
    """Tests for the async workflow tracing."""

    @pytest.mark.asyncio
    async def test_spans_execution(self) -> None:
        """Test that with_tracing wraps workflow execution in a span.

        **Why this test is important:**
          - A workflow orchestrates multiple pipeline steps; without a workflow span the trace has a
            blind spot between the RPC span and the per-step spans — orchestration cost is invisible and
            the step spans have no single workflow parent (the "full tracing" requirement fails).

        **What it tests:**
          - build().with_tracing(tracer) opens exactly one span on execute and returns the result.
        """

        async def orchestrate(x: str) -> str:
            await asyncio.sleep(0)
            return x.upper()

        tracer, _span = _tracer()
        w = (
            AsyncWorkflowBuilder(BaseAsyncWorkflow(orchestrate), "query.answer")
            .with_tracing(tracer)
            .build()
        )

        assert await w.execute("hi") == "HI"
        tracer.span.assert_called_once()

    @pytest.mark.asyncio
    async def test_records_error_on_span(self) -> None:
        """Test that a failing workflow records the error on the span.

        **Why this test is important:**
          - A workflow span that swallows the failure hides the fault from the trace — the orchestration
            would appear to have run cleanly when it did not.

        **What it tests:**
          - An orchestration raising an error records it on the span and re-raises.
        """

        async def failing(_x: str) -> str:
            await asyncio.sleep(0)
            msg = "bad"
            raise InvalidInputError(msg)

        tracer, span = _tracer()
        w = (
            AsyncWorkflowBuilder(BaseAsyncWorkflow(failing), "query.answer")
            .with_tracing(tracer)
            .build()
        )

        with pytest.raises(InvalidInputError):
            await w.execute("x")
        span.record_error.assert_called_once()
