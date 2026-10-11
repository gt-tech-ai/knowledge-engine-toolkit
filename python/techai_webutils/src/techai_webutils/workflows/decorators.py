"""Workflow decorators for cross-cutting concerns.

Provides logging and metrics decorators, and a WorkflowBuilder that composes
them in inside-out order: base → metrics → logging (logging outermost).

Async-first design: WorkflowBuilder delegates to AsyncWorkflowBuilder internally,
then wraps the result in a sync adapter via _run_sync().

The decorator execute-bodies live once in techai_webutils.foundation.decorator;
the public classes here are thin subclasses that bake in the "workflow" tier
labels. The sync/async bridge adapters and the builders stay here —
they are tier-typed.
"""

from __future__ import annotations

import asyncio
import concurrent.futures
from typing import TYPE_CHECKING, Any

from techai_webutils.core.interfaces.workflow import AsyncWorkflow, Workflow
from techai_webutils.foundation.decorator import (
    AsyncLoggingDecoratorBase,
    AsyncMetricsDecoratorBase,
    AsyncRecoveryDecoratorBase,
    AsyncTimeoutDecoratorBase,
    AsyncTracingDecoratorBase,
    LoggingDecoratorBase,
    MetricsDecoratorBase,
)
from techai_webutils.workflows.base import BaseWorkflow

if TYPE_CHECKING:
    from collections.abc import Coroutine

    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
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


class _SyncWorkflowAdapter[In, Out](Workflow[In, Out]):
    """Adapter that wraps AsyncWorkflow and exposes it as sync Workflow.

    Bridges async implementation to sync callers via _run_sync().
    """

    def __init__(self, async_workflow: AsyncWorkflow[In, Out]) -> None:
        """Store the async workflow to bridge to sync callers."""
        self._async_workflow = async_workflow

    def execute(self, input_data: In) -> Out:
        """Execute the async workflow via _run_sync()."""
        return _run_sync(self._async_workflow.execute(input_data))


class _AsyncWorkflowWrapper[In, Out](AsyncWorkflow[In, Out]):
    """Adapter that wraps sync Workflow and exposes it as AsyncWorkflow.

    Allows sync base workflow to be fed into async decorator chain.

    Special handling: If the sync workflow is a BaseWorkflow (which internally
    uses async), we extract its async implementation directly to avoid nested
    asyncio.run() calls. Otherwise, runs sync code in a thread pool to avoid
    blocking the event loop.
    """

    def __init__(self, sync_workflow: Workflow[In, Out]) -> None:
        """Capture the sync workflow, unwrapping a BaseWorkflow's async core.

        For a BaseWorkflow the underlying async implementation is reused
        directly to avoid nested event loops; any other sync workflow is held
        as-is and later run in a thread pool.
        """
        # If the sync workflow is actually BaseWorkflow (async-first design),
        # use its internal async implementation directly
        if isinstance(sync_workflow, BaseWorkflow):
            self._async_workflow = sync_workflow.async_workflow
            self._sync_workflow = None
        else:
            # For other sync workflows, we need to wrap them
            self._async_workflow = None
            self._sync_workflow = sync_workflow

    async def execute(self, input_data: In) -> Out:
        """Execute the workflow in an async context."""
        if self._async_workflow is not None:
            # Use the extracted async implementation
            return await self._async_workflow.execute(input_data)
        if self._sync_workflow is not None:
            # Run sync code in thread pool to avoid blocking event loop
            loop = asyncio.get_event_loop()
            return await loop.run_in_executor(
                None, self._sync_workflow.execute, input_data
            )
        # This should never happen - one of the workflows must be set
        msg = "Internal error: both async and sync workflows are None"
        raise RuntimeError(msg)


class LoggingWorkflowDecorator[In, Out](LoggingDecoratorBase[In, Out], Workflow[In, Out]):
    """Workflow decorator that logs execution start, duration, and errors."""

    def __init__(self, inner: Workflow[In, Out], name: str, logger: Logger) -> None:
        """Wrap inner with "workflow"-labelled logging."""
        super().__init__(inner, name, logger, msg_prefix="workflow", field_key="workflow")


class MetricsWorkflowDecorator[In, Out](MetricsDecoratorBase[In, Out], Workflow[In, Out]):
    """Workflow decorator that records execution count, error count, and duration."""

    def __init__(
        self,
        inner: Workflow[In, Out],
        name: str,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> None:
        """Wrap inner with duration histogram and optional execution/error counters."""
        super().__init__(inner, name, histogram, executions, errors)


class WorkflowBuilder[In, Out]:
    """Builder that composes decorators around a base workflow.

    Decorators are applied inside-out:
    base -> timeout -> metrics -> logging -> recovery.

    Async-first design: Internally uses AsyncWorkflowBuilder to construct the
    decorator chain, then wraps the result in a sync adapter.

    Args:
        base: The base workflow to decorate.
        name: Name used in decorator log/metric labels.

    """

    def __init__(self, base: Workflow[In, Out], name: str) -> None:
        """Initialize the builder with the base workflow and label, no decorators yet."""
        self._base = base
        self._name = name
        self._logger: Logger | None = None
        self._histogram: MetricHistogram | None = None
        self._timeout: float | None = None
        self._recovery: bool = False

    def with_logging(self, logger: Logger) -> WorkflowBuilder[In, Out]:
        """Add a logging decorator."""
        self._logger = logger
        return self

    def with_metrics(self, histogram: MetricHistogram) -> WorkflowBuilder[In, Out]:
        """Add a metrics decorator."""
        self._histogram = histogram
        return self

    def with_timeout(self, seconds: float) -> WorkflowBuilder[In, Out]:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def with_recovery(self) -> WorkflowBuilder[In, Out]:
        """Add a recovery decorator."""
        self._recovery = True
        return self

    def build(self) -> Workflow[In, Out]:
        """Build the decorated workflow using async decorator chain internally."""
        # Wrap sync base as async workflow
        async_base = _AsyncWorkflowWrapper(self._base)

        # Build async decorator chain
        builder = AsyncWorkflowBuilder(async_base, self._name)

        if self._logger is not None:
            builder = builder.with_logging(self._logger)

        if self._histogram is not None:
            builder = builder.with_metrics(self._histogram)

        if self._timeout is not None:
            builder = builder.with_timeout(self._timeout)

        if self._recovery:
            builder = builder.with_recovery()

        async_workflow = builder.build()

        # Wrap async result as sync workflow
        return _SyncWorkflowAdapter(async_workflow)


# ---------------------------------------------------------------------------
# Async workflow decorators
# ---------------------------------------------------------------------------


class LoggingAsyncWorkflowDecorator[In, Out](
    AsyncLoggingDecoratorBase[In, Out], AsyncWorkflow[In, Out]
):
    """Async workflow decorator that logs execution start, duration, and errors."""

    def __init__(self, inner: AsyncWorkflow[In, Out], name: str, logger: Logger) -> None:
        """Wrap inner with "async_workflow"-labelled logging."""
        super().__init__(
            inner, name, logger, msg_prefix="async_workflow", field_key="workflow"
        )


class MetricsAsyncWorkflowDecorator[In, Out](
    AsyncMetricsDecoratorBase[In, Out], AsyncWorkflow[In, Out]
):
    """Async workflow decorator that records execution count, error count, and duration."""

    def __init__(
        self,
        inner: AsyncWorkflow[In, Out],
        name: str,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> None:
        """Wrap inner with duration histogram and optional execution/error counters."""
        super().__init__(inner, name, histogram, executions, errors)


class _TimeoutAsyncWorkflowDecorator[In, Out](
    AsyncTimeoutDecoratorBase[In, Out], AsyncWorkflow[In, Out]
):
    """Async workflow decorator that enforces a timeout."""

    def __init__(self, inner: AsyncWorkflow[In, Out], timeout: float) -> None:
        """Wrap inner with an "async workflow"-labelled timeout."""
        super().__init__(inner, timeout, noun="async workflow")


class _RecoveryAsyncWorkflowDecorator[In, Out](
    AsyncRecoveryDecoratorBase[In, Out], AsyncWorkflow[In, Out]
):
    """Async workflow decorator that normalizes unexpected exceptions."""


class _TracingAsyncWorkflowDecorator[In, Out](
    AsyncTracingDecoratorBase[In, Out], AsyncWorkflow[In, Out]
):
    """Async workflow decorator that runs execution inside a span.

    The workflow span parents the per-step pipeline spans, so one trace shows the whole call stack
    (RPC span → workflow span → pipeline-step spans → leaf client spans) with no gap.
    """

    def __init__(
        self, inner: AsyncWorkflow[In, Out], name: str, tracer: TracerProvider
    ) -> None:
        """Wrap inner with a "workflow"-labelled tracing span."""
        super().__init__(
            inner, name, tracer, span_prefix="workflow", field_key="workflow"
        )


class AsyncWorkflowBuilder[In, Out]:
    """Builder that composes decorators around a base async workflow.

    Decorators are applied inside-out:
    base -> timeout -> metrics -> tracing -> logging -> recovery.

    Args:
        base: The base async workflow to decorate.
        name: Name used in decorator log/metric labels.

    """

    def __init__(self, base: AsyncWorkflow[In, Out], name: str) -> None:
        """Initialize the builder with the base async workflow and label, no decorators yet."""
        self._base = base
        self._name = name
        self._logger: Logger | None = None
        self._histogram: MetricHistogram | None = None
        self._tracer: TracerProvider | None = None
        self._timeout: float | None = None
        self._recovery: bool = False

    def with_logging(self, logger: Logger) -> AsyncWorkflowBuilder[In, Out]:
        """Add a logging decorator."""
        self._logger = logger
        return self

    def with_metrics(self, histogram: MetricHistogram) -> AsyncWorkflowBuilder[In, Out]:
        """Add a metrics decorator."""
        self._histogram = histogram
        return self

    def with_tracing(self, tracer: TracerProvider) -> AsyncWorkflowBuilder[In, Out]:
        """Add a tracing decorator (one workflow span per execute, errors recorded on the span)."""
        self._tracer = tracer
        return self

    def with_timeout(self, seconds: float) -> AsyncWorkflowBuilder[In, Out]:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def with_recovery(self) -> AsyncWorkflowBuilder[In, Out]:
        """Add a recovery decorator."""
        self._recovery = True
        return self

    def build(self) -> AsyncWorkflow[In, Out]:
        """Build the decorated async workflow."""
        w: AsyncWorkflow[In, Out] = self._base

        if self._timeout is not None:
            w = _TimeoutAsyncWorkflowDecorator(w, self._timeout)

        if self._histogram is not None:
            w = MetricsAsyncWorkflowDecorator(w, self._name, self._histogram)

        if self._tracer is not None:
            w = _TracingAsyncWorkflowDecorator(w, self._name, self._tracer)

        if self._logger is not None:
            w = LoggingAsyncWorkflowDecorator(w, self._name, self._logger)

        if self._recovery:
            w = _RecoveryAsyncWorkflowDecorator(w)

        return w
