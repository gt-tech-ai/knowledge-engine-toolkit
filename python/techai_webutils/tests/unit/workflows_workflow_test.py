"""Tests for the workflow layer: base implementations and decorators."""

from __future__ import annotations

import asyncio
import time
from unittest.mock import MagicMock

import pytest

from techai_webutils.workflows.base import BaseAsyncWorkflow, BaseWorkflow
from techai_webutils.workflows.decorators import AsyncWorkflowBuilder, WorkflowBuilder


class TestBaseWorkflow:
    """Test suite for the synchronous BaseWorkflow."""

    def test_execute_wraps_callable(self) -> None:
        """Test that BaseWorkflow wraps a callable and delegates to it.

        **Why this test is important:**
          - BaseWorkflow is the foundation for all synchronous multi-step orchestration
          - Incorrect delegation would silently corrupt workflow outputs
          - The callable contract is the core abstraction that all workflow decorators depend on

        **What it tests:**
          - Callable result is returned from execute
        """
        orchestrate = lambda x: x + "-processed"  # noqa: E731
        w = BaseWorkflow(orchestrate)
        result = w.execute("order")
        assert result == "order-processed"

    def test_execute_propagates_errors(self) -> None:
        """Test that errors from the wrapped callable propagate through the workflow.

        **Why this test is important:**
          - Workflow errors must not be silently swallowed, or orchestration will appear to succeed
          - Error propagation is essential for retry and recovery decorators to function correctly
          - Callers depend on exceptions to detect and handle orchestration failures

        **What it tests:**
          - ValueError raised by the callable is re-raised from execute
        """

        def failing(_x: str) -> str:
            msg = "orchestration failed"
            raise ValueError(msg)

        w = BaseWorkflow(failing)
        with pytest.raises(ValueError, match="orchestration failed"):
            w.execute("input")


class TestBaseAsyncWorkflow:
    """Test suite for the asynchronous BaseAsyncWorkflow."""

    @pytest.mark.asyncio
    async def test_execute_wraps_async_callable(self) -> None:
        """Test that BaseAsyncWorkflow wraps an async callable and delegates to it.

        **Why this test is important:**
          - BaseAsyncWorkflow enables non-blocking multi-step orchestration in async services
          - The async contract must correctly await the callable and return its result
          - Incorrect async handling could cause coroutine leaks or unresolved futures

        **What it tests:**
          - Async callable result is returned from execute
        """

        async def orchestrate(x: str) -> str:
            await asyncio.sleep(0)
            return x + "-processed"

        w = BaseAsyncWorkflow(orchestrate)
        result = await w.execute("order")
        assert result == "order-processed"

    @pytest.mark.asyncio
    async def test_execute_propagates_errors(self) -> None:
        """Test that errors from the wrapped async callable propagate through the workflow.

        **Why this test is important:**
          - Async error propagation is different from sync and must be explicitly verified
          - Swallowed async exceptions would cause silent orchestration failures
          - Retry and recovery decorators depend on exceptions being properly raised

        **What it tests:**
          - ValueError raised by the async callable is re-raised from execute
        """

        async def failing(_x: str) -> str:
            await asyncio.sleep(0)
            msg = "async orchestration failed"
            raise ValueError(msg)

        w = BaseAsyncWorkflow(failing)
        with pytest.raises(ValueError, match="async orchestration failed"):
            await w.execute("input")


class TestLoggingDecorator:
    """Test suite for the logging workflow decorator via builder."""

    def test_logs_execution(self) -> None:
        """Test that the logging decorator logs workflow execution start.

        **Why this test is important:**
          - Workflow logging is essential for tracing multi-step orchestration in production
          - The decorator must include the workflow name for log correlation across steps
          - Missing logs would make orchestration failures invisible during incident response

        **What it tests:**
          - Result is returned correctly through the decorator
          - Logger debug is called with the workflow name
        """
        logger = MagicMock()
        orchestrate = lambda x: x + "-done"  # noqa: E731
        w = (
            WorkflowBuilder(BaseWorkflow(orchestrate), "test-workflow")
            .with_logging(logger)
            .build()
        )

        result = w.execute("task")
        assert result == "task-done"

        logger.debug.assert_called()
        call_args = logger.debug.call_args
        assert "test-workflow" in str(call_args)

    def test_logs_errors(self) -> None:
        """Test that the logging decorator logs errors at error level.

        **Why this test is important:**
          - Workflow errors must be logged at error level for alerting and monitoring
          - The workflow name must be included for identifying which orchestration failed
          - Error-level logging enables automated detection of workflow degradation

        **What it tests:**
          - ValueError propagates through the decorator
          - Logger error is called with the workflow name
        """
        logger = MagicMock()

        def failing(_x: str) -> str:
            msg = "workflow error"
            raise ValueError(msg)

        w = (
            WorkflowBuilder(BaseWorkflow(failing), "error-workflow")
            .with_logging(logger)
            .build()
        )

        with pytest.raises(ValueError, match="workflow error"):
            w.execute("input")

        logger.exception.assert_called()
        call_args = logger.exception.call_args
        assert "error-workflow" in str(call_args)


class TestWorkflowBuilder:
    """Test suite for the workflow decorator builder."""

    def test_builder_composition(self) -> None:
        """Test that the builder composes multiple decorators correctly.

        **Why this test is important:**
          - Production workflows use multiple decorators (logging, metrics, recovery) simultaneously
          - The builder's fluent API must produce a correct decorator chain
          - Incorrect composition could cause decorators to be skipped or applied in wrong order

        **What it tests:**
          - Orchestration result is returned through composed decorators
          - Logger debug is called
        """
        logger = MagicMock()
        orchestrate = lambda x: "composed-" + x  # noqa: E731

        w = (
            WorkflowBuilder(BaseWorkflow(orchestrate), "composed-workflow")
            .with_logging(logger)
            .build()
        )

        result = w.execute("test")
        assert result == "composed-test"
        logger.debug.assert_called()

    def test_builder_without_decorators(self) -> None:
        """Test that builder with no decorators returns the base workflow.

        **Why this test is important:**
          - The builder must work correctly even with no decorators applied
          - This is the baseline behavior that all decorator tests build upon
          - Ensures the builder does not introduce unintended side effects when empty

        **What it tests:**
          - Orchestration result is returned unmodified
        """
        orchestrate = lambda x: x + "-done"  # noqa: E731
        w = WorkflowBuilder(BaseWorkflow(orchestrate), "bare-workflow").build()

        result = w.execute("task")
        assert result == "task-done"


class TestMetricsDecorator:
    """Test suite for the metrics workflow decorator."""

    def test_metrics_records_duration(self) -> None:
        """Test that the metrics decorator records execution duration via histogram.

        **Why this test is important:**
          - Workflow duration metrics are essential for performance monitoring and SLO tracking
          - The histogram must receive a positive duration for meaningful latency analysis
          - Missing metrics would leave workflow performance invisible to observability tools

        **What it tests:**
          - Result is returned correctly through the decorator
          - Histogram observe is called once with a positive duration
        """
        histogram = MagicMock()
        orchestrate = lambda x: x + "-done"  # noqa: E731

        w = (
            WorkflowBuilder(BaseWorkflow(orchestrate), "metrics-workflow")
            .with_metrics(histogram)
            .build()
        )

        result = w.execute("task")
        assert result == "task-done"
        histogram.observe.assert_called_once()
        args = histogram.observe.call_args
        assert args[0][0] > 0

    def test_metrics_records_on_error(self) -> None:
        """Test that metrics decorator records duration even when workflow raises.

        **Why this test is important:**
          - Failed workflow executions are often the most important to measure
          - Error latency metrics reveal whether failures are fast (validation) or slow (timeout)
          - Missing error metrics would create blind spots in performance dashboards

        **What it tests:**
          - ValueError propagates through the decorator
          - Histogram observe is still called once despite the error
        """
        histogram = MagicMock()

        def failing(_x: str) -> str:
            msg = "metrics error"
            raise ValueError(msg)

        w = (
            WorkflowBuilder(BaseWorkflow(failing), "error-metrics")
            .with_metrics(histogram)
            .build()
        )

        with pytest.raises(ValueError, match="metrics error"):
            w.execute("input")

        histogram.observe.assert_called_once()

    def test_builder_full_composition(self) -> None:
        """Test that builder composes logging and metrics correctly.

        **Why this test is important:**
          - Logging + metrics is the standard decorator combination for production workflows
          - Both decorators must fire on every execution without interfering with each other
          - Verifies the builder produces the correct inside-out decorator ordering

        **What it tests:**
          - Orchestration result is returned through composed decorators
          - Logger debug is called
          - Histogram observe is called once
        """
        logger = MagicMock()
        histogram = MagicMock()
        orchestrate = lambda x: "full-" + x  # noqa: E731

        w = (
            WorkflowBuilder(BaseWorkflow(orchestrate), "full-workflow")
            .with_logging(logger)
            .with_metrics(histogram)
            .build()
        )

        result = w.execute("test")
        assert result == "full-test"
        logger.debug.assert_called()
        histogram.observe.assert_called_once()


# ---------------------------------------------------------------------------
# Resilience decorator tests
# ---------------------------------------------------------------------------


class TestWorkflowTimeoutDecorator:
    """Test suite for the timeout workflow decorator."""

    def test_timeout_passes_on_fast_op(self) -> None:
        """Test that sync workflow with timeout succeeds for fast operations.

        **Why this test is important:**
          - The timeout decorator must not interfere with normal-speed workflow operations
          - A generous timeout should be invisible to the caller
          - Ensures the decorator correctly passes through results without alteration

        **What it tests:**
          - Workflow returns the orchestrated result when completing before the deadline
        """
        orchestrate = lambda x: x + "-done"  # noqa: E731
        w = (
            WorkflowBuilder(BaseWorkflow(orchestrate), "fast-workflow")
            .with_timeout(5.0)
            .build()
        )

        result = w.execute("task")
        assert result == "task-done"

    def test_timeout_raises_on_slow_op(self) -> None:
        """Test that sync workflow with timeout raises AppTimeoutError for slow operations.

        **Why this test is important:**
          - Unbounded workflow operations can block worker threads and degrade the system
          - The timeout decorator prevents slow orchestration from consuming resources indefinitely
          - AppTimeoutError is classified separately for proper client-side error handling

        **What it tests:**
          - Slow workflow triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        def slow(x: str) -> str:
            time.sleep(2.0)
            return x + "-done"

        w = (
            WorkflowBuilder(BaseWorkflow(slow), "slow-workflow")
            .with_timeout(0.01)
            .build()
        )

        with pytest.raises(AppTimeoutError):
            w.execute("task")


class TestWorkflowRecoveryDecorator:
    """Test suite for the recovery workflow decorator."""

    def test_recovery_catches_exception(self) -> None:
        """Test that recovery wraps unexpected exceptions as InternalError.

        **Why this test is important:**
          - Unexpected exceptions in workflow steps must not crash the service
          - The recovery decorator provides a safety net for unhandled orchestration errors
          - Preserving the original cause enables debugging while keeping the API response clean

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        from techai_webutils.core.errors.errors import InternalError

        def failing(_x: str) -> str:
            msg = "unexpected boom"
            raise RuntimeError(msg)

        w = (
            WorkflowBuilder(BaseWorkflow(failing), "recover-workflow")
            .with_recovery()
            .build()
        )

        with pytest.raises(InternalError) as exc_info:
            w.execute("input")
        assert exc_info.value.cause is not None

    def test_recovery_passes_through_app_error(self) -> None:
        """Test that recovery re-raises AppError as-is.

        **Why this test is important:**
          - Known application errors (validation, not-found, etc.) must not be wrapped
          - Double-wrapping AppErrors would lose the specific error type and HTTP status mapping
          - The recovery decorator must distinguish between expected and unexpected errors

        **What it tests:**
          - InvalidInputError is re-raised without being wrapped
        """
        from techai_webutils.core.errors.errors import InvalidInputError

        def failing(_x: str) -> str:
            msg = "bad input"
            raise InvalidInputError(msg)

        w = (
            WorkflowBuilder(BaseWorkflow(failing), "app-error-workflow")
            .with_recovery()
            .build()
        )

        with pytest.raises(InvalidInputError):
            w.execute("input")


class TestSyncWorkflowInsideRunningLoop:
    """Test that sync workflows work when called from within a running event loop."""

    @pytest.mark.asyncio
    async def test_base_workflow_inside_running_loop(self) -> None:
        """Test that BaseWorkflow.execute() works inside a running event loop.

        **Why this test is important:**
          - Before the _run_sync fix, calling BaseWorkflow.execute() from within
            an async context raised RuntimeError: asyncio.run() cannot be called
            from a running event loop.
          - The thread-based fallback must transparently handle this scenario.

        **What it tests:**
          - BaseWorkflow.execute() returns the correct result when called from async code
        """
        orchestrate = lambda x: x + "-processed"  # noqa: E731
        w = BaseWorkflow(orchestrate)
        result = w.execute("order")
        assert result == "order-processed"

    @pytest.mark.asyncio
    async def test_workflow_builder_inside_running_loop(self) -> None:
        """Test that WorkflowBuilder-built workflow works inside a running event loop.

        **Why this test is important:**
          - The _SyncWorkflowAdapter returned by WorkflowBuilder.build() also uses
            _run_sync, and must work inside a running event loop.

        **What it tests:**
          - Built workflow returns the correct result from async code
          - Logger decorator fires correctly
        """
        logger = MagicMock()
        orchestrate = lambda x: x + "-done"  # noqa: E731
        w = (
            WorkflowBuilder(BaseWorkflow(orchestrate), "loop-test")
            .with_logging(logger)
            .build()
        )

        result = w.execute("task")
        assert result == "task-done"
        logger.debug.assert_called()

    @pytest.mark.asyncio
    async def test_base_workflow_error_inside_running_loop(self) -> None:
        """Test that errors propagate correctly from sync workflow inside a running loop.

        **Why this test is important:**
          - The thread-based bridge must propagate exceptions, not swallow them.

        **What it tests:**
          - ValueError raised by the callable propagates through _run_sync
        """

        def failing(_x: str) -> str:
            msg = "loop error"
            raise ValueError(msg)

        w = BaseWorkflow(failing)
        with pytest.raises(ValueError, match="loop error"):
            w.execute("input")


class TestAsyncWorkflowResilienceDecorators:
    """Test suite for async workflow timeout, recovery, and full chain."""

    @pytest.mark.asyncio
    async def test_async_workflow_timeout(self) -> None:
        """Test that async workflow timeout raises AppTimeoutError for slow operations.

        **Why this test is important:**
          - Async workflows must enforce timeouts to prevent coroutine leaks
          - The timeout decorator must correctly cancel the awaited coroutine
          - Ensures async timeout behavior mirrors sync timeout behavior

        **What it tests:**
          - Slow async workflow triggers AppTimeoutError when timeout is exceeded
        """
        import asyncio

        from techai_webutils.core.errors.errors import AppTimeoutError

        async def slow(x: str) -> str:
            await asyncio.sleep(2.0)
            return x + "-done"

        w = (
            AsyncWorkflowBuilder(BaseAsyncWorkflow(slow), "async-slow")
            .with_timeout(0.01)
            .build()
        )

        with pytest.raises(AppTimeoutError):
            await w.execute("task")

    @pytest.mark.asyncio
    async def test_async_workflow_recovery(self) -> None:
        """Test that async workflow recovery wraps unexpected exceptions as InternalError.

        **Why this test is important:**
          - Async exception handling differs from sync and must be explicitly verified
          - The recovery decorator must correctly catch exceptions from awaited coroutines
          - Preserving the cause enables debugging of async-specific failures

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        from techai_webutils.core.errors.errors import InternalError

        async def failing(_x: str) -> str:
            await asyncio.sleep(0)
            msg = "async unexpected"
            raise RuntimeError(msg)

        w = (
            AsyncWorkflowBuilder(BaseAsyncWorkflow(failing), "async-recover")
            .with_recovery()
            .build()
        )

        with pytest.raises(InternalError) as exc_info:
            await w.execute("input")
        assert exc_info.value.cause is not None

    @pytest.mark.asyncio
    async def test_async_workflow_full_chain(self) -> None:
        """Test that async workflow with timeout, metrics, logging, and recovery all compose.

        **Why this test is important:**
          - Production async workflows use the full decorator chain simultaneously
          - All decorators must compose without interfering with each other in async context
          - This integration-style test catches ordering and interaction bugs between decorators

        **What it tests:**
          - Orchestration result is returned through the full decorator chain
          - Logger debug is called
          - Histogram observe is called once
        """

        async def orchestrate(x: str) -> str:
            await asyncio.sleep(0)
            return "full-" + x

        logger = MagicMock()
        histogram = MagicMock()
        w = (
            AsyncWorkflowBuilder(BaseAsyncWorkflow(orchestrate), "full-async")
            .with_timeout(5.0)
            .with_metrics(histogram)
            .with_logging(logger)
            .with_recovery()
            .build()
        )

        result = await w.execute("test")
        assert result == "full-test"
        logger.debug.assert_called()
        histogram.observe.assert_called_once()
