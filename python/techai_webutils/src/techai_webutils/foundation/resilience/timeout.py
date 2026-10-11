"""Async timeout wrapper.

Wraps an awaitable with a timeout, raising TimeoutError if exceeded.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppTimeoutError

if TYPE_CHECKING:
    from collections.abc import Awaitable


async def with_timeout[T](coro: Awaitable[T], seconds: float) -> T:
    """Execute an awaitable with a timeout.

    Args:
        coro: The coroutine to execute.
        seconds: Maximum time in seconds to wait.

    Returns:
        The result of the coroutine.

    Raises:
        TimeoutError: If the coroutine exceeds the timeout.

    """
    try:
        return await asyncio.wait_for(coro, timeout=seconds)
    except TimeoutError as e:
        msg = f"operation timed out after {seconds}s"
        raise AppTimeoutError(msg) from e
