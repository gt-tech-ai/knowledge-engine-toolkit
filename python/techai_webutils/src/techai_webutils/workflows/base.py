"""Base workflow implementations.

Provides BaseWorkflow and BaseAsyncWorkflow that wrap callables and implement
the Workflow/AsyncWorkflow interfaces from core.

Async-first design: BaseWorkflow wraps BaseAsyncWorkflow and bridges via _run_sync().
"""

from __future__ import annotations

import asyncio
import concurrent.futures
from typing import TYPE_CHECKING, Any

from techai_webutils.core.interfaces.workflow import AsyncWorkflow, Workflow

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Coroutine


def _run_sync[T](coro: Coroutine[Any, Any, T]) -> T:
    """Bridge async coroutine to sync, safe even inside a running event loop."""
    try:
        asyncio.get_running_loop()
    except RuntimeError:
        return asyncio.run(coro)
    else:
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
            return pool.submit(asyncio.run, coro).result()


class BaseWorkflow[In, Out](Workflow[In, Out]):
    """Workflow that wraps a synchronous callable.

    Provides a functional escape hatch for simple workflows, though workflows
    are typically stateful (holding service/pipeline/cache references).

    Async-first design: Internally converts the sync callable to async (running it
    in a thread pool to avoid blocking the event loop) and delegates to
    BaseAsyncWorkflow, bridging via _run_sync().

    Args:
        fn: A callable that orchestrates In to Out.

    """

    def __init__(self, fn: Callable[[In], Out]) -> None:
        """Wrap a sync callable and delegate to an async implementation."""

        # Wrap the sync callable as an async coroutine that runs in thread pool
        # to avoid blocking the event loop (important for timeout/cancellation)
        async def async_fn(input_data: In) -> Out:
            """Run the sync callable in the default executor."""
            loop = asyncio.get_event_loop()
            return await loop.run_in_executor(None, fn, input_data)

        # Delegate to async implementation
        self._async = BaseAsyncWorkflow(async_fn)

    @property
    def async_workflow(self) -> BaseAsyncWorkflow[In, Out]:
        """The async implementation this sync workflow delegates to.

        The async decorator chain reuses it directly, so a sync base workflow never nests
        event loops.
        """
        return self._async

    def execute(self, input_data: In) -> Out:
        """Orchestrate by delegating to async workflow via _run_sync()."""
        return _run_sync(self._async.execute(input_data))


class BaseAsyncWorkflow[In, Out](AsyncWorkflow[In, Out]):
    """Workflow that wraps an asynchronous callable.

    This is the primary variant since Python services are typically async.

    Args:
        fn: An async callable that orchestrates In to Out.

    """

    def __init__(self, fn: Callable[[In], Awaitable[Out]]) -> None:
        """Store the wrapped async callable for later execution."""
        self._fn = fn

    async def execute(self, input_data: In) -> Out:
        """Orchestrate by delegating to the wrapped async callable."""
        return await self._fn(input_data)
