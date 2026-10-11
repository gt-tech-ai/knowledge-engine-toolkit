"""Tests for the OpService builder — single-operation decorated services.

The Python analog of Go's ``go/services/builder.go``: a domain service exposes ONE decoratable
``run`` chokepoint (unlike CRUD's five methods), and the builder wraps it with the cross-cutting
decorator stack (logging + timeout + recovery, recovery outermost) that every app service adopts.
"""

from __future__ import annotations

import asyncio
from unittest.mock import MagicMock, create_autospec

import pytest

from techai_webutils.core.errors.errors import (
    AppTimeoutError,
    InternalError,
    InvalidInputError,
)
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricHistogram,
    MetricsProvider,
)
from techai_webutils.core.interfaces.op_service import OpService
from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan
from techai_webutils.services.op_builder import build


async def _echo(args: str) -> str:
    """Echo the args (a trivial operation)."""
    await asyncio.sleep(0)
    return f"result:{args}"


class TestOpServiceBuilder:
    """Tests for the op service builder."""

    @pytest.mark.asyncio
    async def test_builds_named_runnable_service(self) -> None:
        """Test that the builder produces a named OpService whose run executes the operation.

        **Why this test is important:**
          - The single ``run`` chokepoint + name is the contract every decorator and the transport
            layer depend on; a wrong name or dropped result breaks logs/metrics correlation.

        **What it tests:**
          - build(run).named("echo").service() yields an OpService with name "echo" that runs.
        """
        svc = build(_echo).named("echo").service()
        assert isinstance(svc, OpService)
        assert svc.name == "echo"
        assert await svc.run("x") == "result:x"

    @pytest.mark.asyncio
    async def test_logging_decorator_logs_entry_and_failure(self) -> None:
        """Test that the logging decorator logs operation entry (debug) and failure.

        **Why this test is important:**
          - Observability of every service operation is the whole point of the decorator stack;
            silent operations can't be debugged in production.

        **What it tests:**
          - A failing run logs entry at debug and the failure via the logger, then re-raises.
        """
        logger = create_autospec(Logger, instance=True)

        async def boom(_args: str) -> str:
            await asyncio.sleep(0)
            msg = "bad"
            raise InvalidInputError(msg)

        svc = build(boom).named("op").with_log(logger).service()
        with pytest.raises(InvalidInputError):
            await svc.run("x")
        logger.debug.assert_called()
        logger.exception.assert_called()

    @pytest.mark.asyncio
    async def test_recovery_wraps_unexpected_exception(self) -> None:
        """Test that recovery converts an unexpected exception into InternalError.

        **Why this test is important:**
          - Recovery is the always-outermost guard (mirrors Go's panic recovery); an unhandled
            exception must not escape the service boundary as an opaque type.

        **What it tests:**
          - A run raising a plain ValueError surfaces as InternalError.
        """

        async def boom(_args: str) -> str:
            await asyncio.sleep(0)
            msg = "unexpected"
            raise ValueError(msg)

        svc = build(boom).named("op").service()
        with pytest.raises(InternalError):
            await svc.run("x")

    @pytest.mark.asyncio
    async def test_recovery_passes_apperror_through(self) -> None:
        """Test that a domain AppError passes through recovery unchanged.

        **What it tests:**
          - A run raising InvalidInputError re-raises as InvalidInputError (not wrapped).
        """

        async def boom(_args: str) -> str:
            await asyncio.sleep(0)
            msg = "bad"
            raise InvalidInputError(msg)

        svc = build(boom).named("op").service()
        with pytest.raises(InvalidInputError):
            await svc.run("x")

    @pytest.mark.asyncio
    async def test_timeout_decorator(self) -> None:
        """Test that the timeout decorator aborts a slow operation with AppTimeoutError.

        **Why this test is important:**
          - Resiliency: a hung operation must not block the caller forever; the timeout is the
            service-layer bound.

        **What it tests:**
          - A run that sleeps past the timeout raises AppTimeoutError.
        """

        async def slow(_args: str) -> str:
            await asyncio.sleep(1)
            return "done"

        svc = build(slow).named("op").with_timeout(0.01).service()
        with pytest.raises(AppTimeoutError):
            await svc.run("x")

    @pytest.mark.asyncio
    async def test_metrics_decorator_records_success_and_error(self) -> None:
        """Test that the metrics decorator counts operations by result and observes duration.

        **Why this test is important:**
          - RED metrics per service operation are how prod dashboards see rate/errors/duration; a
            missing metric hides regressions.

        **What it tests:**
          - A successful run increments the counter with result=success and observes the histogram;
            a failing run increments result=error.
        """
        metrics = create_autospec(MetricsProvider, instance=True)
        counter = create_autospec(MetricCounter, instance=True)
        hist = create_autospec(MetricHistogram, instance=True)
        metrics.counter.return_value = counter
        metrics.histogram.return_value = hist

        svc = build(_echo).named("op").with_metrics(metrics).service()
        await svc.run("x")
        counter.inc.assert_called_with(service="op", result="success")
        hist.observe.assert_called()

        async def boom(_args: str) -> str:
            await asyncio.sleep(0)
            msg = "bad"
            raise InvalidInputError(msg)

        svc_err = build(boom).named("op").with_metrics(metrics).service()
        with pytest.raises(InvalidInputError):
            await svc_err.run("x")
        counter.inc.assert_called_with(service="op", result="error")

    @pytest.mark.asyncio
    async def test_tracing_decorator_spans_and_records_error(self) -> None:
        """Test that the tracing decorator opens a span per operation and records failures on it.

        **Why this test is important:**
          - Distributed traces need a span per service operation; without it, a slow/failed call is
            invisible in the trace waterfall.

        **What it tests:**
          - A run opens a span named for the service; a failing run records the error on the span.
        """
        tracer = create_autospec(TracerProvider, instance=True)
        span = create_autospec(TracerSpan, instance=True)
        cm = MagicMock()
        cm.__enter__.return_value = span
        cm.__exit__.return_value = False
        tracer.span.return_value = cm

        svc = build(_echo).named("op").with_tracing(tracer).service()
        await svc.run("x")
        tracer.span.assert_called()

        async def boom(_args: str) -> str:
            await asyncio.sleep(0)
            msg = "bad"
            raise InvalidInputError(msg)

        svc_err = build(boom).named("op").with_tracing(tracer).service()
        with pytest.raises(InvalidInputError):
            await svc_err.run("x")
        span.record_error.assert_called()

    @pytest.mark.asyncio
    async def test_full_stack_composes(self) -> None:
        """Test that the full decorator stack composes (metrics + tracing + logging + timeout + recovery).

        **What it tests:**
          - build(run).named().with_log().with_metrics().with_tracing().with_timeout().service()
            runs successfully with every decorator active.
        """
        metrics = create_autospec(MetricsProvider, instance=True)
        metrics.counter.return_value = create_autospec(MetricCounter, instance=True)
        metrics.histogram.return_value = create_autospec(MetricHistogram, instance=True)
        tracer = create_autospec(TracerProvider, instance=True)
        cm = MagicMock()
        cm.__enter__.return_value = create_autospec(TracerSpan, instance=True)
        cm.__exit__.return_value = False
        tracer.span.return_value = cm
        logger = create_autospec(Logger, instance=True)

        svc = (
            build(_echo)
            .named("full")
            .with_log(logger)
            .with_metrics(metrics)
            .with_tracing(tracer)
            .with_timeout(5.0)
            .service()
        )
        assert await svc.run("x") == "result:x"
