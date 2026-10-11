"""Tests for the pipeline layer: base implementations and decorators."""

from __future__ import annotations

import asyncio
import time
from unittest.mock import MagicMock

import pytest

from techai_webutils.pipelines.base import BaseAsyncPipeline, BasePipeline
from techai_webutils.pipelines.decorators import AsyncPipelineBuilder, PipelineBuilder


class TestBasePipeline:
    """Test suite for the synchronous BasePipeline."""

    def test_execute_wraps_callable(self) -> None:
        """Test that BasePipeline wraps a callable and delegates to it.

        **Why this test is important:**
          - BasePipeline is the foundation for all synchronous data transformation steps
          - Incorrect delegation would silently corrupt data flowing through the pipeline
          - The callable contract is the core abstraction that all pipeline decorators depend on

        **What it tests:**
          - Callable result is returned from execute
        """
        transform = lambda x: x.upper()  # noqa: E731
        p = BasePipeline(transform)
        result = p.execute("hello")
        assert result == "HELLO"

    def test_execute_propagates_errors(self) -> None:
        """Test that errors from the wrapped callable propagate through the pipeline.

        **Why this test is important:**
          - Pipeline errors must not be silently swallowed, or data processing will appear to succeed
          - Error propagation is essential for retry and recovery decorators to function correctly
          - Callers depend on exceptions to detect and handle transformation failures

        **What it tests:**
          - ValueError raised by the callable is re-raised from execute
        """

        def failing(_x: str) -> str:
            msg = "transform failed"
            raise ValueError(msg)

        p = BasePipeline(failing)
        with pytest.raises(ValueError, match="transform failed"):
            p.execute("input")


class TestBaseAsyncPipeline:
    """Test suite for the asynchronous BaseAsyncPipeline."""

    @pytest.mark.asyncio
    async def test_execute_wraps_async_callable(self) -> None:
        """Test that BaseAsyncPipeline wraps an async callable and delegates to it.

        **Why this test is important:**
          - BaseAsyncPipeline enables non-blocking data transformations in async services
          - The async contract must correctly await the callable and return its result
          - Incorrect async handling could cause coroutine leaks or unresolved futures

        **What it tests:**
          - Async callable result is returned from execute
        """

        async def transform(x: str) -> str:
            await asyncio.sleep(0)
            return x.upper()

        p = BaseAsyncPipeline(transform)
        result = await p.execute("hello")
        assert result == "HELLO"

    @pytest.mark.asyncio
    async def test_execute_propagates_errors(self) -> None:
        """Test that errors from the wrapped async callable propagate through the pipeline.

        **Why this test is important:**
          - Async error propagation is different from sync and must be explicitly verified
          - Swallowed async exceptions would cause silent data processing failures
          - Retry and recovery decorators depend on exceptions being properly raised

        **What it tests:**
          - ValueError raised by the async callable is re-raised from execute
        """

        async def failing(_x: str) -> str:
            await asyncio.sleep(0)
            msg = "async transform failed"
            raise ValueError(msg)

        p = BaseAsyncPipeline(failing)
        with pytest.raises(ValueError, match="async transform failed"):
            await p.execute("input")


class TestLoggingDecorator:
    """Test suite for the logging pipeline decorator via builder."""

    def test_logs_execution(self) -> None:
        """Test that the logging decorator logs pipeline execution start and completion.

        **Why this test is important:**
          - Pipeline logging is essential for tracing data transformation flow in production
          - The decorator must include the pipeline name for log correlation across stages
          - Missing logs would make pipeline failures invisible during incident response

        **What it tests:**
          - Result is returned correctly through the decorator
          - Logger debug is called with the pipeline name
        """
        logger = MagicMock()
        transform = lambda x: x.upper()  # noqa: E731
        p = (
            PipelineBuilder(BasePipeline(transform), "test-pipeline")
            .with_logging(logger)
            .build()
        )

        result = p.execute("hello")
        assert result == "HELLO"

        # Verify debug was called (execution start)
        logger.debug.assert_called()
        call_args = logger.debug.call_args
        assert "test-pipeline" in str(call_args)

    def test_logs_errors(self) -> None:
        """Test that the logging decorator logs errors at error level.

        **Why this test is important:**
          - Pipeline errors must be logged at error level for alerting and monitoring
          - The pipeline name must be included for identifying which stage failed
          - Error-level logging enables automated detection of pipeline degradation

        **What it tests:**
          - ValueError propagates through the decorator
          - Logger error is called with the pipeline name
        """
        logger = MagicMock()

        def failing(_x: str) -> str:
            msg = "pipeline error"
            raise ValueError(msg)

        p = (
            PipelineBuilder(BasePipeline(failing), "error-pipeline")
            .with_logging(logger)
            .build()
        )

        with pytest.raises(ValueError, match="pipeline error"):
            p.execute("input")

        # Verify error was logged
        logger.exception.assert_called()
        call_args = logger.exception.call_args
        assert "error-pipeline" in str(call_args)


class TestSyncLoggingDecorator:
    """Test the sync LoggingPipelineDecorator directly (not via async builder)."""

    def test_sync_logging_success(self) -> None:
        """Test sync logging decorator logs and returns on success.

        **Why this test is important:**
          - The sync LoggingPipelineDecorator is a public API for direct use
          - Must log execution and return result correctly

        **What it tests:**
          - Sync decorator returns transform result
          - Logger debug is called
        """
        from techai_webutils.pipelines.decorators import LoggingPipelineDecorator

        logger = MagicMock()
        base = BasePipeline(lambda x: x.upper())
        decorated = LoggingPipelineDecorator(base, "sync-test", logger)

        result = decorated.execute("hello")

        assert result == "HELLO"
        logger.debug.assert_called()

    def test_sync_logging_error(self) -> None:
        """Test sync logging decorator logs errors.

        **Why this test is important:**
          - Errors must be logged at error level for operational visibility
          - The original exception must propagate unchanged

        **What it tests:**
          - Error propagates through the decorator
          - Logger error is called
        """
        from techai_webutils.pipelines.decorators import LoggingPipelineDecorator

        logger = MagicMock()

        def failing(_x: str) -> str:
            msg = "boom"
            raise ValueError(msg)

        decorated = LoggingPipelineDecorator(BasePipeline(failing), "sync-err", logger)

        with pytest.raises(ValueError, match="boom"):
            decorated.execute("input")

        logger.exception.assert_called()


class TestPipelineBuilder:
    """Test suite for the pipeline decorator builder."""

    def test_builder_composition(self) -> None:
        """Test that the builder composes multiple decorators correctly.

        **Why this test is important:**
          - Production pipelines use multiple decorators (logging, metrics, recovery) simultaneously
          - The builder's fluent API must produce a correct decorator chain
          - Incorrect composition could cause decorators to be skipped or applied in wrong order

        **What it tests:**
          - Transform result is returned through composed decorators
          - Logger debug is called
        """
        logger = MagicMock()
        transform = lambda x: "composed-" + x  # noqa: E731

        p = (
            PipelineBuilder(BasePipeline(transform), "composed-pipeline")
            .with_logging(logger)
            .build()
        )

        result = p.execute("test")
        assert result == "composed-test"
        logger.debug.assert_called()

    def test_builder_error_flow(self) -> None:
        """Test that errors flow through composed decorators.

        **Why this test is important:**
          - Errors must propagate through the entire decorator chain for proper handling
          - Each decorator must re-raise after performing its cross-cutting concern
          - Swallowed errors in any decorator would break the error handling contract

        **What it tests:**
          - ValueError propagates through composed decorators
          - Logger error is called on failure
        """
        logger = MagicMock()

        def failing(_x: str) -> str:
            msg = "composed error"
            raise ValueError(msg)

        p = (
            PipelineBuilder(BasePipeline(failing), "error-composed")
            .with_logging(logger)
            .build()
        )

        with pytest.raises(ValueError, match="composed error"):
            p.execute("input")

        logger.exception.assert_called()

    def test_builder_without_decorators(self) -> None:
        """Test that building without decorators returns a passthrough pipeline.

        **Why this test is important:**
          - The builder must work correctly even with no decorators applied
          - This is the baseline behavior that all decorator tests build upon
          - Ensures the builder does not introduce unintended side effects when empty

        **What it tests:**
          - Transform result is returned unmodified
        """
        transform = lambda x: x.upper()  # noqa: E731
        p = PipelineBuilder(BasePipeline(transform), "bare-pipeline").build()

        result = p.execute("hello")
        assert result == "HELLO"


class TestMetricsDecorator:
    """Test suite for the metrics pipeline decorator."""

    def test_metrics_records_duration(self) -> None:
        """Test that the metrics decorator records execution duration via histogram.

        **Why this test is important:**
          - Pipeline duration metrics are essential for performance monitoring and SLO tracking
          - The histogram must receive a positive duration for meaningful latency analysis
          - Missing metrics would leave pipeline performance invisible to observability tools

        **What it tests:**
          - Result is returned correctly through the decorator
          - Histogram observe is called once with a positive duration
        """
        histogram = MagicMock()
        transform = lambda x: x.upper()  # noqa: E731

        p = (
            PipelineBuilder(BasePipeline(transform), "metrics-pipeline")
            .with_metrics(histogram)
            .build()
        )

        result = p.execute("hello")
        assert result == "HELLO"
        histogram.observe.assert_called_once()
        # Duration should be a positive float
        args = histogram.observe.call_args
        assert args[0][0] > 0  # first positional arg is duration

    def test_metrics_records_on_error(self) -> None:
        """Test that metrics decorator records duration even when pipeline raises.

        **Why this test is important:**
          - Failed pipeline executions are often the most important to measure
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

        p = (
            PipelineBuilder(BasePipeline(failing), "error-metrics")
            .with_metrics(histogram)
            .build()
        )

        with pytest.raises(ValueError, match="metrics error"):
            p.execute("input")

        histogram.observe.assert_called_once()

    def test_builder_full_composition(self) -> None:
        """Test that builder composes logging and metrics correctly.

        **Why this test is important:**
          - Logging + metrics is the standard decorator combination for production pipelines
          - Both decorators must fire on every execution without interfering with each other
          - Verifies the builder produces the correct inside-out decorator ordering

        **What it tests:**
          - Transform result is returned through composed decorators
          - Logger debug is called
          - Histogram observe is called once
        """
        logger = MagicMock()
        histogram = MagicMock()
        transform = lambda x: "full-" + x  # noqa: E731

        p = (
            PipelineBuilder(BasePipeline(transform), "full-pipeline")
            .with_logging(logger)
            .with_metrics(histogram)
            .build()
        )

        result = p.execute("test")
        assert result == "full-test"
        logger.debug.assert_called()
        histogram.observe.assert_called_once()


# ---------------------------------------------------------------------------
# Resilience decorator tests
# ---------------------------------------------------------------------------


class TestPipelineTimeoutDecorator:
    """Test suite for the timeout pipeline decorator."""

    def test_timeout_passes_on_fast_op(self) -> None:
        """Test that sync pipeline with timeout succeeds for fast operations.

        **Why this test is important:**
          - The timeout decorator must not interfere with normal-speed pipeline operations
          - A generous timeout should be invisible to the caller
          - Ensures the decorator correctly passes through results without alteration

        **What it tests:**
          - Pipeline returns the transformed result when completing before the deadline
        """
        transform = lambda x: x.upper()  # noqa: E731
        p = (
            PipelineBuilder(BasePipeline(transform), "fast-pipeline")
            .with_timeout(5.0)
            .build()
        )

        result = p.execute("hello")
        assert result == "HELLO"

    def test_timeout_raises_on_slow_op(self) -> None:
        """Test that sync pipeline with timeout raises AppTimeoutError for slow operations.

        **Why this test is important:**
          - Unbounded pipeline operations can block worker threads and degrade the system
          - The timeout decorator prevents slow transformations from consuming resources indefinitely
          - AppTimeoutError is classified separately for proper client-side error handling

        **What it tests:**
          - Slow pipeline triggers AppTimeoutError when timeout is exceeded
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        def slow(x: str) -> str:
            time.sleep(2.0)
            return x.upper()

        p = (
            PipelineBuilder(BasePipeline(slow), "slow-pipeline")
            .with_timeout(0.01)
            .build()
        )

        with pytest.raises(AppTimeoutError):
            p.execute("hello")


class TestPipelineRecoveryDecorator:
    """Test suite for the recovery pipeline decorator."""

    def test_recovery_catches_exception(self) -> None:
        """Test that recovery wraps unexpected exceptions as InternalError.

        **Why this test is important:**
          - Unexpected exceptions in pipeline stages must not crash the service
          - The recovery decorator provides a safety net for unhandled transformation errors
          - Preserving the original cause enables debugging while keeping the API response clean

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        from techai_webutils.core.errors.errors import InternalError

        def failing(_x: str) -> str:
            msg = "unexpected boom"
            raise RuntimeError(msg)

        p = (
            PipelineBuilder(BasePipeline(failing), "recover-pipeline")
            .with_recovery()
            .build()
        )

        with pytest.raises(InternalError) as exc_info:
            p.execute("input")
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

        p = (
            PipelineBuilder(BasePipeline(failing), "app-error-pipeline")
            .with_recovery()
            .build()
        )

        with pytest.raises(InvalidInputError):
            p.execute("input")


class TestSyncPipelineInsideRunningLoop:
    """Test that sync pipelines work when called from within a running event loop."""

    @pytest.mark.asyncio
    async def test_base_pipeline_inside_running_loop(self) -> None:
        """Test that BasePipeline.execute() works inside a running event loop.

        **Why this test is important:**
          - Before the _run_sync fix, calling BasePipeline.execute() from within
            an async context raised RuntimeError: asyncio.run() cannot be called
            from a running event loop.
          - The thread-based fallback must transparently handle this scenario.

        **What it tests:**
          - BasePipeline.execute() returns the correct result when called from async code
        """
        transform = lambda x: x.upper()  # noqa: E731
        p = BasePipeline(transform)
        result = p.execute("hello")
        assert result == "HELLO"

    @pytest.mark.asyncio
    async def test_pipeline_builder_inside_running_loop(self) -> None:
        """Test that PipelineBuilder-built pipeline works inside a running event loop.

        **Why this test is important:**
          - The _SyncPipelineAdapter returned by PipelineBuilder.build() also uses
            _run_sync, and must work inside a running event loop.

        **What it tests:**
          - Built pipeline returns the correct result from async code
          - Logger decorator fires correctly
        """
        logger = MagicMock()
        transform = lambda x: x.upper()  # noqa: E731
        p = (
            PipelineBuilder(BasePipeline(transform), "loop-test")
            .with_logging(logger)
            .build()
        )

        result = p.execute("hello")
        assert result == "HELLO"
        logger.debug.assert_called()

    @pytest.mark.asyncio
    async def test_base_pipeline_error_inside_running_loop(self) -> None:
        """Test that errors propagate correctly from sync pipeline inside a running loop.

        **Why this test is important:**
          - The thread-based bridge must propagate exceptions, not swallow them.

        **What it tests:**
          - ValueError raised by the callable propagates through _run_sync
        """

        def failing(_x: str) -> str:
            msg = "loop error"
            raise ValueError(msg)

        p = BasePipeline(failing)
        with pytest.raises(ValueError, match="loop error"):
            p.execute("input")


class TestAsyncPipelineResilienceDecorators:
    """Test suite for async pipeline timeout, recovery, and full chain."""

    @pytest.mark.asyncio
    async def test_async_pipeline_timeout(self) -> None:
        """Test that async pipeline timeout raises AppTimeoutError for slow operations.

        **Why this test is important:**
          - Async pipelines must enforce timeouts to prevent coroutine leaks
          - The timeout decorator must correctly cancel the awaited coroutine
          - Ensures async timeout behavior mirrors sync timeout behavior

        **What it tests:**
          - Slow async pipeline triggers AppTimeoutError when timeout is exceeded
        """
        import asyncio

        from techai_webutils.core.errors.errors import AppTimeoutError

        async def slow(x: str) -> str:
            await asyncio.sleep(2.0)
            return x.upper()

        p = (
            AsyncPipelineBuilder(BaseAsyncPipeline(slow), "async-slow")
            .with_timeout(0.01)
            .build()
        )

        with pytest.raises(AppTimeoutError):
            await p.execute("hello")

    @pytest.mark.asyncio
    async def test_async_pipeline_recovery(self) -> None:
        """Test that async pipeline recovery wraps unexpected exceptions as InternalError.

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

        p = (
            AsyncPipelineBuilder(BaseAsyncPipeline(failing), "async-recover")
            .with_recovery()
            .build()
        )

        with pytest.raises(InternalError) as exc_info:
            await p.execute("input")
        assert exc_info.value.cause is not None

    @pytest.mark.asyncio
    async def test_async_pipeline_full_chain(self) -> None:
        """Test that async pipeline with timeout, metrics, logging, and recovery all compose.

        **Why this test is important:**
          - Production async pipelines use the full decorator chain simultaneously
          - All decorators must compose without interfering with each other in async context
          - This integration-style test catches ordering and interaction bugs between decorators

        **What it tests:**
          - Transform result is returned through the full decorator chain
          - Logger debug is called
          - Histogram observe is called once
        """

        async def transform(x: str) -> str:
            await asyncio.sleep(0)
            return "full-" + x

        logger = MagicMock()
        histogram = MagicMock()
        p = (
            AsyncPipelineBuilder(BaseAsyncPipeline(transform), "full-async")
            .with_timeout(5.0)
            .with_metrics(histogram)
            .with_logging(logger)
            .with_recovery()
            .build()
        )

        result = await p.execute("test")
        assert result == "full-test"
        logger.debug.assert_called()
        histogram.observe.assert_called_once()
