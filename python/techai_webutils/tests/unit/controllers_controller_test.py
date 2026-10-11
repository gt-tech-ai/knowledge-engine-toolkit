"""Tests for the controller layer: base controller and handler decorators."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from unittest.mock import MagicMock

import pytest

from techai_webutils.controllers.base import BaseController
from techai_webutils.controllers.decorators import HandlerBuilder
from techai_webutils.core.interfaces.rate_limiter import RateLimiter


@dataclass
class SampleRequest:
    """A request DTO the handler-builder tests decode."""

    input: str


@dataclass
class SampleResponse:
    """A response DTO the handler-builder tests encode."""

    output: str


class TestBaseController:
    """Test suite for the BaseController base class."""

    def test_handle_error_returns_structured_dict(self) -> None:
        """Test that handle_error returns a structured error response.

        **Why this test is important:**
          - All controller error responses flow through handle_error
          - The structured dict format is consumed by HTTP serialization middleware
          - Missing or incorrect fields would cause response serialization failures

        **What it tests:**
          - Response contains an 'error' key
          - Error value matches the exception message
        """
        ctrl = BaseController()
        result = ctrl.handle_error(ValueError("something broke"))
        assert "error" in result
        assert result["error"] == "something broke"

    def test_handle_error_includes_error_type(self) -> None:
        """Test that handle_error includes the exception type name.

        **Why this test is important:**
          - The exception type name is used for error classification in monitoring dashboards
          - Clients may use the type field to implement specific error handling strategies
          - Missing type information would make error categorization impossible

        **What it tests:**
          - Response 'type' field equals the exception class name
        """
        ctrl = BaseController()
        result = ctrl.handle_error(RuntimeError("runtime issue"))
        assert result["type"] == "RuntimeError"


class TestHandlerExecution:
    """Test suite for basic handler execution via builder."""

    @pytest.mark.asyncio
    async def test_handler_execution(self) -> None:
        """Test that handler executes and returns result.

        **Why this test is important:**
          - The handler builder is the entry point for all request processing
          - Incorrect execution would break every endpoint in the service
          - The request-response contract must be verified at the most basic level

        **What it tests:**
          - Handler output matches the expected transformation of input
        """

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="processed-" + req.input)

        built = HandlerBuilder(handler, "test-handler").build()
        result = await built(SampleRequest(input="test"))
        assert result.output == "processed-test"

    @pytest.mark.asyncio
    async def test_handler_error_propagation(self) -> None:
        """Test that errors from handlers propagate through decorators.

        **Why this test is important:**
          - Handler errors must reach the error handling middleware to produce proper HTTP responses
          - Swallowed errors would cause silent failures visible only as empty or default responses
          - Error propagation is required for recovery and logging decorators to function

        **What it tests:**
          - ValueError raised by the handler is re-raised from the built handler
        """

        async def handler(_req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            msg = "handler failed"
            raise ValueError(msg)

        built = HandlerBuilder(handler, "error-handler").build()
        with pytest.raises(ValueError, match="handler failed"):
            await built(SampleRequest(input="test"))


class TestLoggingDecorator:
    """Test suite for the logging handler decorator."""

    @pytest.mark.asyncio
    async def test_logs_execution(self) -> None:
        """Test that the logging decorator logs handler execution.

        **Why this test is important:**
          - Handler logging is essential for tracing request processing in production
          - The decorator must include the handler name for log correlation across endpoints
          - Missing logs would make request failures invisible during incident response

        **What it tests:**
          - Result is returned correctly through the decorator
          - Logger debug is called with the handler name
        """
        logger = MagicMock()

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="output-" + req.input)

        built = HandlerBuilder(handler, "test-handler").with_logging(logger).build()

        result = await built(SampleRequest(input="test"))
        assert result.output == "output-test"

        logger.debug.assert_called()
        call_args = logger.debug.call_args
        assert "test-handler" in str(call_args)

    @pytest.mark.asyncio
    async def test_logs_errors(self) -> None:
        """Test that the logging decorator logs errors at error level.

        **Why this test is important:**
          - Handler errors must be logged at error level for alerting and monitoring
          - The handler name must be included for identifying which endpoint failed
          - Error-level logging enables automated detection of endpoint degradation

        **What it tests:**
          - ValueError propagates through the decorator
          - Logger error is called
        """
        logger = MagicMock()

        async def handler(_req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            msg = "handler error"
            raise ValueError(msg)

        built = HandlerBuilder(handler, "error-handler").with_logging(logger).build()

        with pytest.raises(ValueError, match="handler error"):
            await built(SampleRequest(input="test"))

        logger.exception.assert_called()


class TestRecoveryDecorator:
    """Test suite for the recovery handler decorator."""

    @pytest.mark.asyncio
    async def test_wraps_unexpected_as_internal_error(self) -> None:
        """Test that recovery decorator wraps non-AppError exceptions as InternalError.

        **Why this test is important:**
          - Unhandled exceptions in handlers would crash the request processing pipeline
          - The recovery decorator is the last line of defense against uncaught errors
          - Raising InternalError preserves the type contract (Resp type) instead of
            returning a dict that breaks the generic type system
          - Matches Go's ``wrapRecovery`` which returns ``apperr.Internal(...)``

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Original exception is preserved as the cause
        """
        from techai_webutils.core.errors.errors import InternalError

        async def handler(_req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            msg = "unexpected error"
            raise RuntimeError(msg)

        built = HandlerBuilder(handler, "panic-handler").with_recovery().build()

        with pytest.raises(InternalError) as exc_info:
            await built(SampleRequest(input="test"))
        assert exc_info.value.cause is not None

    @pytest.mark.asyncio
    async def test_passes_app_errors_through(self) -> None:
        """Test that recovery decorator re-raises AppError subclasses as-is.

        **Why this test is important:**
          - Domain errors (NotFound, Forbidden, etc.) carry specific error codes and HTTP mappings
          - Wrapping them in InternalError would lose the original error semantics
          - Callers depend on receiving the correct AppError subclass for error handling

        **What it tests:**
          - NotFoundError passes through the recovery decorator unchanged
        """
        from techai_webutils.core.errors.errors import NotFoundError

        async def handler(_req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            msg = "resource missing"
            raise NotFoundError(msg)

        built = HandlerBuilder(handler, "notfound-handler").with_recovery().build()

        with pytest.raises(NotFoundError, match="resource missing"):
            await built(SampleRequest(input="test"))


class TestMetricsDecorator:
    """Test suite for the metrics handler decorator."""

    @pytest.mark.asyncio
    async def test_metrics_records_duration(self) -> None:
        """Test that the metrics decorator records execution duration via histogram.

        **Why this test is important:**
          - Handler duration metrics are essential for endpoint latency monitoring
          - The histogram must receive a positive duration for meaningful SLO tracking
          - Missing metrics would leave endpoint performance invisible to observability tools

        **What it tests:**
          - Result is returned correctly through the decorator
          - Histogram observe is called once with a positive duration
        """
        histogram = MagicMock()

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="metrics-" + req.input)

        built = HandlerBuilder(handler, "metrics-handler").with_metrics(histogram).build()

        result = await built(SampleRequest(input="test"))
        assert result.output == "metrics-test"
        histogram.observe.assert_called_once()
        args = histogram.observe.call_args
        assert args[0][0] > 0

    @pytest.mark.asyncio
    async def test_metrics_records_on_error(self) -> None:
        """Test that metrics decorator records duration even when handler raises.

        **Why this test is important:**
          - Failed request metrics are often the most important to measure
          - Error latency metrics reveal whether failures are fast (validation) or slow (timeout)
          - Missing error metrics would create blind spots in endpoint dashboards

        **What it tests:**
          - ValueError propagates through the decorator
          - Histogram observe is still called once despite the error
        """
        histogram = MagicMock()

        async def handler(_req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            msg = "metrics error"
            raise ValueError(msg)

        built = HandlerBuilder(handler, "error-metrics").with_metrics(histogram).build()

        with pytest.raises(ValueError, match="metrics error"):
            await built(SampleRequest(input="test"))

        histogram.observe.assert_called_once()


class TestHandlerBuilder:
    """Test suite for the handler decorator builder."""

    @pytest.mark.asyncio
    async def test_builder_composition(self) -> None:
        """Test that the builder composes multiple decorators correctly.

        **Why this test is important:**
          - Production handlers use multiple decorators (logging, metrics, recovery) simultaneously
          - The builder's fluent API must produce a correct decorator chain
          - Incorrect composition could cause decorators to be skipped or applied in wrong order

        **What it tests:**
          - Handler result is returned through composed decorators
          - Logger debug is called
        """
        logger = MagicMock()

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="composed-" + req.input)

        built = (
            HandlerBuilder(handler, "composed-handler")
            .with_logging(logger)
            .with_recovery()
            .build()
        )

        result = await built(SampleRequest(input="test"))
        assert result.output == "composed-test"
        logger.debug.assert_called()

    @pytest.mark.asyncio
    async def test_builder_recovery_with_logging(self) -> None:
        """Test that recovery wraps exceptions as InternalError and logging still works.

        **Why this test is important:**
          - Recovery + logging is a common production combination
          - The logging decorator must log even when recovery wraps an exception
          - This verifies decorators compose correctly in error paths

        **What it tests:**
          - RuntimeError is caught and wrapped as InternalError
          - Logger error is called (from the logging decorator)
        """
        from techai_webutils.core.errors.errors import InternalError

        logger = MagicMock()

        async def handler(_req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            msg = "boom"
            raise RuntimeError(msg)

        built = (
            HandlerBuilder(handler, "error-composed")
            .with_logging(logger)
            .with_recovery()
            .build()
        )

        with pytest.raises(InternalError):
            await built(SampleRequest(input="test"))
        logger.exception.assert_called()

    @pytest.mark.asyncio
    async def test_builder_full_composition(self) -> None:
        """Test that builder composes logging, metrics, and recovery correctly.

        **Why this test is important:**
          - The full decorator chain is the standard production configuration
          - All decorators must fire on every execution without interfering with each other
          - Verifies the builder produces the correct inside-out decorator ordering

        **What it tests:**
          - Handler result is returned through the full decorator chain
          - Logger debug is called
          - Histogram observe is called once
        """
        logger = MagicMock()
        histogram = MagicMock()

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="full-" + req.input)

        built = (
            HandlerBuilder(handler, "full-handler")
            .with_logging(logger)
            .with_metrics(histogram)
            .with_recovery()
            .build()
        )

        result = await built(SampleRequest(input="test"))
        assert result.output == "full-test"
        logger.debug.assert_called()
        histogram.observe.assert_called_once()


# ---------------------------------------------------------------------------
# Resilience decorator tests
# ---------------------------------------------------------------------------


def _rate_limiter(*, allow: bool) -> MagicMock:
    """Return a mock RateLimiter whose non-blocking ``allow()`` reports the given gate decision."""
    limiter = MagicMock(spec=RateLimiter)
    limiter.allow.return_value = allow
    return limiter


class TestHandlerTimeoutDecorator:
    """Test suite for the timeout handler decorator."""

    @pytest.mark.asyncio
    async def test_timeout_passes_on_fast_handler(self) -> None:
        """Test that handler completes within timeout and returns result.

        **Why this test is important:**
          - The timeout decorator must not interfere with normal-speed handler operations
          - A generous timeout should be invisible to the caller
          - Ensures the decorator correctly passes through results without alteration

        **What it tests:**
          - Handler returns the expected response when completing before the deadline
        """

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="fast-" + req.input)

        built = HandlerBuilder(handler, "fast-handler").with_timeout(5.0).build()
        result = await built(SampleRequest(input="test"))
        assert result.output == "fast-test"

    @pytest.mark.asyncio
    async def test_timeout_raises_on_slow_handler(self) -> None:
        """Test that slow handler raises AppTimeoutError when deadline exceeded.

        **Why this test is important:**
          - Unbounded handler operations can exhaust thread pools and degrade the system
          - The timeout decorator prevents slow request processing from consuming resources indefinitely
          - AppTimeoutError is classified separately for proper client-side error handling

        **What it tests:**
          - Slow handler triggers AppTimeoutError when timeout is exceeded
        """
        import asyncio

        from techai_webutils.core.errors.errors import AppTimeoutError

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(2.0)
            return SampleResponse(output="slow-" + req.input)

        built = HandlerBuilder(handler, "slow-handler").with_timeout(0.01).build()

        with pytest.raises(AppTimeoutError):
            await built(SampleRequest(input="test"))


class TestHandlerRateLimitDecorator:
    """Test suite for the rate-limit handler decorator."""

    @pytest.mark.asyncio
    async def test_rate_limit_allows_within_limit(self) -> None:
        """Test that handler proceeds when rate limiter allows the request.

        **Why this test is important:**
          - Rate limiting protects services from traffic spikes and abuse
          - Allowed requests must pass through to the handler without modification
          - Verifies the rate limiter integration does not interfere with normal operation

        **What it tests:**
          - Handler result is returned when rate limiter allows the request
        """

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="allowed-" + req.input)

        limiter = _rate_limiter(allow=True)
        built = HandlerBuilder(handler, "rate-handler").with_rate_limit(limiter).build()  # type: ignore[arg-type]
        result = await built(SampleRequest(input="test"))
        assert result.output == "allowed-test"

    @pytest.mark.asyncio
    async def test_rate_limit_rejects_when_exceeded(self) -> None:
        """Test that handler raises UnavailableError when rate limiter denies the request.

        **Why this test is important:**
          - Exceeded rate limits must immediately reject requests to protect downstream services
          - UnavailableError maps to HTTP 503, signaling clients to retry with backoff
          - The handler must not execute when the rate limit is exceeded

        **What it tests:**
          - UnavailableError is raised when rate limiter denies the request
        """
        from techai_webutils.core.errors.errors import UnavailableError

        async def handler(_req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="should-not-reach")

        limiter = _rate_limiter(allow=False)
        built = HandlerBuilder(handler, "rate-handler").with_rate_limit(limiter).build()  # type: ignore[arg-type]

        with pytest.raises(UnavailableError):
            await built(SampleRequest(input="test"))


class TestHandlerFullResilienceChain:
    """Test suite for full handler resilience chain composition."""

    @pytest.mark.asyncio
    async def test_full_resilience_chain(self) -> None:
        """Test that timeout, rate_limit, metrics, logging, and recovery all compose.

        **Why this test is important:**
          - The full resilience chain is the standard production handler configuration
          - All decorators must compose without interfering with each other
          - This integration-style test catches ordering and interaction bugs between decorators

        **What it tests:**
          - Handler result is returned through the full resilience chain
          - Logger debug is called
          - Histogram observe is called once
        """
        logger = MagicMock()
        histogram = MagicMock()
        limiter = _rate_limiter(allow=True)

        async def handler(req: SampleRequest) -> SampleResponse:
            await asyncio.sleep(0)
            return SampleResponse(output="resilient-" + req.input)

        built = (
            HandlerBuilder(handler, "resilient-handler")
            .with_timeout(5.0)
            .with_rate_limit(limiter)  # type: ignore[arg-type]
            .with_metrics(histogram)
            .with_logging(logger)
            .with_recovery()
            .build()
        )

        result = await built(SampleRequest(input="test"))
        assert result.output == "resilient-test"
        logger.debug.assert_called()
        histogram.observe.assert_called_once()
