"""Base pipeline implementations.

Provides BasePipeline and BaseAsyncPipeline that wrap callables and implement
the Pipeline/AsyncPipeline interfaces from core.

Async-first design: BasePipeline wraps BaseAsyncPipeline and bridges via _run_sync().
"""

from __future__ import annotations

import asyncio
import concurrent.futures
from typing import TYPE_CHECKING, Any

from techai_webutils.core.interfaces.pipeline import AsyncPipeline, Pipeline

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


class BasePipeline[In, Out](Pipeline[In, Out]):
    """Pipeline that wraps a synchronous callable.

    This provides a simple way to create pipelines from stateless functions.

    Async-first design: Internally converts the sync callable to async (running it
    in a thread pool to avoid blocking the event loop) and delegates to
    BaseAsyncPipeline, bridging via _run_sync().

    Args:
        fn: A callable that transforms In to Out.

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
        self._async = BaseAsyncPipeline(async_fn)

    @property
    def async_pipeline(self) -> BaseAsyncPipeline[In, Out]:
        """The async implementation this sync pipeline delegates to.

        The async decorator chain reuses it directly, so a sync base pipeline never nests
        event loops.
        """
        return self._async

    def execute(self, input_data: In) -> Out:
        """Transform input by delegating to async pipeline via _run_sync()."""
        return _run_sync(self._async.execute(input_data))


class BaseAsyncPipeline[In, Out](AsyncPipeline[In, Out]):
    """Pipeline that wraps an asynchronous callable.

    This provides a simple way to create async pipelines from stateless async functions.

    Args:
        fn: An async callable that transforms In to Out.

    """

    def __init__(self, fn: Callable[[In], Awaitable[Out]]) -> None:
        """Store the wrapped async callable for later execution."""
        self._fn = fn

    async def execute(self, input_data: In) -> Out:
        """Transform input by delegating to the wrapped async callable."""
        return await self._fn(input_data)
