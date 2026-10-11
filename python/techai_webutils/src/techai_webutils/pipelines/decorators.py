"""Pipeline decorators for cross-cutting concerns.

Provides logging and metrics decorators, and a PipelineBuilder that composes
them in inside-out order: base → metrics → logging (logging outermost).

Async-first design: PipelineBuilder delegates to AsyncPipelineBuilder internally,
then wraps the result in a sync adapter via _run_sync().

The decorator execute-bodies live once in techai_webutils.foundation.decorator;
the public classes here are thin subclasses that bake in the "pipeline" tier
labels. The sync/async bridge adapters and the builders stay here —
they are tier-typed.
"""

from __future__ import annotations

import asyncio
import concurrent.futures
from typing import TYPE_CHECKING, Any

from techai_webutils.core.interfaces.pipeline import AsyncPipeline, Pipeline
from techai_webutils.foundation.decorator import (
    AsyncLoggingDecoratorBase,
    AsyncMetricsDecoratorBase,
    AsyncRecoveryDecoratorBase,
    AsyncTimeoutDecoratorBase,
    AsyncTracingDecoratorBase,
    LoggingDecoratorBase,
    MetricsDecoratorBase,
)
from techai_webutils.pipelines.base import BasePipeline

if TYPE_CHECKING:
    from collections.abc import Coroutine

    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricHistogram
    from techai_webutils.core.interfaces.tracer import TracerProvider


def _run_sync[T](coro: Coroutine[Any, Any, T]) -> T:
    """Bridge async coroutine to sync, safe even inside a running event loop."""
    try:
        asyncio.get_running_loop()
    except RuntimeError:
        return asyncio.run(coro)
    else:
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            return pool.submit(asyncio.run, coro).result()


class _SyncPipelineAdapter[In, Out](Pipeline[In, Out]):
    """Adapter that wraps AsyncPipeline and exposes it as sync Pipeline.

    Bridges async implementation to sync callers via _run_sync().
    """

    def __init__(self, async_pipeline: AsyncPipeline[In, Out]) -> None:
        """Store the async pipeline to bridge to sync callers."""
        self._async_pipeline = async_pipeline

    def execute(self, input_data: In) -> Out:
        """Execute the async pipeline via _run_sync()."""
        return _run_sync(self._async_pipeline.execute(input_data))


class _AsyncPipelineWrapper[In, Out](AsyncPipeline[In, Out]):
    """Adapter that wraps sync Pipeline and exposes it as AsyncPipeline.

    Allows sync base pipeline to be fed into async decorator chain.

    Special handling: If the sync pipeline is a BasePipeline (which internally
    uses async), we extract its async implementation directly to avoid nested
    asyncio.run() calls. Otherwise, runs sync code in a thread pool to avoid
    blocking the event loop.
    """

    def __init__(self, sync_pipeline: Pipeline[In, Out]) -> None:
        """Capture the sync pipeline, unwrapping a BasePipeline's async core.

        For a BasePipeline the underlying async implementation is reused
        directly to avoid nested event loops; any other sync pipeline is held
        as-is and later run in a thread pool.
        """
        # If the sync pipeline is actually BasePipeline (async-first design),
        # use its internal async implementation directly
        if isinstance(sync_pipeline, BasePipeline):
            self._async_pipeline = sync_pipeline.async_pipeline
            self._sync_pipeline = None
        else:
            # For other sync pipelines, we need to wrap them
            self._async_pipeline = None
            self._sync_pipeline = sync_pipeline

    async def execute(self, input_data: In) -> Out:
        """Execute the pipeline in an async context."""
        if self._async_pipeline is not None:
            # Use the extracted async implementation
            return await self._async_pipeline.execute(input_data)
        if self._sync_pipeline is not None:
            # Run sync code in thread pool to avoid blocking event loop
            loop = asyncio.get_event_loop()
            return await loop.run_in_executor(
                None, self._sync_pipeline.execute, input_data
            )
        # This should never happen - one of the pipelines must be set
        msg = "Internal error: both async and sync pipelines are None"
        raise RuntimeError(msg)


class LoggingPipelineDecorator[In, Out](LoggingDecoratorBase[In, Out], Pipeline[In, Out]):
    """Pipeline decorator that logs execution start, duration, and errors."""

    def __init__(self, inner: Pipeline[In, Out], name: str, logger: Logger) -> None:
        """Wrap inner with "pipeline"-labelled logging."""
        super().__init__(inner, name, logger, msg_prefix="pipeline", field_key="pipeline")


class MetricsPipelineDecorator[In, Out](MetricsDecoratorBase[In, Out], Pipeline[In, Out]):
    """Pipeline decorator that records execution duration via histogram."""

    def __init__(
        self, inner: Pipeline[In, Out], name: str, histogram: MetricHistogram
    ) -> None:
        """Wrap inner with duration-histogram metrics."""
        super().__init__(inner, name, histogram)


class PipelineBuilder[In, Out]:
    """Builder that composes decorators around a base pipeline.

    Decorators are applied inside-out:
    base -> timeout -> metrics -> logging -> recovery.

    Async-first design: Internally uses AsyncPipelineBuilder to construct the
    decorator chain, then wraps the result in a sync adapter.

    Args:
        base: The base pipeline to decorate.
        name: Name used in decorator log/metric labels.

    """

    def __init__(self, base: Pipeline[In, Out], name: str) -> None:
        """Initialize the builder with the base pipeline and label, no decorators yet."""
        self._base = base
        self._name = name
        self._logger: Logger | None = None
        self._histogram: MetricHistogram | None = None
        self._timeout: float | None = None
        self._recovery: bool = False

    def with_logging(self, logger: Logger) -> PipelineBuilder[In, Out]:
        """Add a logging decorator."""
        self._logger = logger
        return self

    def with_metrics(self, histogram: MetricHistogram) -> PipelineBuilder[In, Out]:
        """Add a metrics decorator."""
        self._histogram = histogram
        return self

    def with_timeout(self, seconds: float) -> PipelineBuilder[In, Out]:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def with_recovery(self) -> PipelineBuilder[In, Out]:
        """Add a recovery decorator."""
        self._recovery = True
        return self

    def build(self) -> Pipeline[In, Out]:
        """Build the decorated pipeline using async decorator chain internally."""
        # Wrap sync base as async pipeline
        async_base = _AsyncPipelineWrapper(self._base)

        # Build async decorator chain
        builder = AsyncPipelineBuilder(async_base, self._name)

        if self._logger is not None:
            builder = builder.with_logging(self._logger)

        if self._histogram is not None:
            builder = builder.with_metrics(self._histogram)

        if self._timeout is not None:
            builder = builder.with_timeout(self._timeout)

        if self._recovery:
            builder = builder.with_recovery()

        async_pipeline = builder.build()

        # Wrap async result as sync pipeline
        return _SyncPipelineAdapter(async_pipeline)


# ---------------------------------------------------------------------------
# Async pipeline decorators
# ---------------------------------------------------------------------------


class LoggingAsyncPipelineDecorator[In, Out](
    AsyncLoggingDecoratorBase[In, Out], AsyncPipeline[In, Out]
):
    """Async pipeline decorator that logs execution start, duration, and errors."""

    def __init__(self, inner: AsyncPipeline[In, Out], name: str, logger: Logger) -> None:
        """Wrap inner with "async_pipeline"-labelled logging."""
        super().__init__(
            inner, name, logger, msg_prefix="async_pipeline", field_key="pipeline"
        )


class MetricsAsyncPipelineDecorator[In, Out](
    AsyncMetricsDecoratorBase[In, Out], AsyncPipeline[In, Out]
):
    """Async pipeline decorator that records execution duration via histogram."""

    def __init__(
        self, inner: AsyncPipeline[In, Out], name: str, histogram: MetricHistogram
    ) -> None:
        """Wrap inner with duration-histogram metrics."""
        super().__init__(inner, name, histogram)


class _TimeoutAsyncPipelineDecorator[In, Out](
    AsyncTimeoutDecoratorBase[In, Out], AsyncPipeline[In, Out]
):
    """Async pipeline decorator that enforces a timeout."""

    def __init__(self, inner: AsyncPipeline[In, Out], timeout: float) -> None:
        """Wrap inner with an "async pipeline"-labelled timeout."""
        super().__init__(inner, timeout, noun="async pipeline")


class _RecoveryAsyncPipelineDecorator[In, Out](
    AsyncRecoveryDecoratorBase[In, Out], AsyncPipeline[In, Out]
):
    """Async pipeline decorator that normalizes unexpected exceptions."""


class _TracingAsyncPipelineDecorator[In, Out](
    AsyncTracingDecoratorBase[In, Out], AsyncPipeline[In, Out]
):
    """Async pipeline decorator that runs execution inside a span."""

    def __init__(
        self, inner: AsyncPipeline[In, Out], name: str, tracer: TracerProvider
    ) -> None:
        """Wrap inner with a "pipeline"-labelled tracing span."""
        super().__init__(
            inner, name, tracer, span_prefix="pipeline", field_key="pipeline"
        )


class AsyncPipelineBuilder[In, Out]:
    """Builder that composes decorators around a base async pipeline.

    Decorators are applied inside-out:
    base -> timeout -> metrics -> logging -> recovery.

    Args:
        base: The base async pipeline to decorate.
        name: Name used in decorator log/metric labels.

    """

    def __init__(self, base: AsyncPipeline[In, Out], name: str) -> None:
        """Initialize the builder with the base async pipeline and label, no decorators yet."""
        self._base = base
        self._name = name
        self._logger: Logger | None = None
        self._histogram: MetricHistogram | None = None
        self._tracer: TracerProvider | None = None
        self._timeout: float | None = None
        self._recovery: bool = False

    def with_logging(self, logger: Logger) -> AsyncPipelineBuilder[In, Out]:
        """Add a logging decorator."""
        self._logger = logger
        return self

    def with_metrics(self, histogram: MetricHistogram) -> AsyncPipelineBuilder[In, Out]:
        """Add a metrics decorator."""
        self._histogram = histogram
        return self

    def with_tracing(self, tracer: TracerProvider) -> AsyncPipelineBuilder[In, Out]:
        """Add a tracing decorator (one span per execute, errors recorded on the span)."""
        self._tracer = tracer
        return self

    def with_timeout(self, seconds: float) -> AsyncPipelineBuilder[In, Out]:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def with_recovery(self) -> AsyncPipelineBuilder[In, Out]:
        """Add a recovery decorator."""
        self._recovery = True
        return self

    def build(self) -> AsyncPipeline[In, Out]:
        """Build the decorated async pipeline (base → timeout → metrics → tracing → logging → recovery)."""
        p: AsyncPipeline[In, Out] = self._base

        if self._timeout is not None:
            p = _TimeoutAsyncPipelineDecorator(p, self._timeout)

        if self._histogram is not None:
            p = MetricsAsyncPipelineDecorator(p, self._name, self._histogram)

        if self._tracer is not None:
            p = _TracingAsyncPipelineDecorator(p, self._name, self._tracer)

        if self._logger is not None:
            p = LoggingAsyncPipelineDecorator(p, self._name, self._logger)

        if self._recovery:
            p = _RecoveryAsyncPipelineDecorator(p)

        return p
