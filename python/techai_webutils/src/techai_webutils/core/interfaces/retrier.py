"""Retrier interface for automatic retry on transient failures.

Mirrors Go's ``interfaces.Retrier``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


class Retrier(ABC):
    """Executes operations with automatic retry on transient failures."""

    @abstractmethod
    async def retry(self, op: Callable[[], Awaitable[object]]) -> object:
        """Execute op with automatic retry on transient failures.

        Returns the result on success, or raises the last error after all
        retries are exhausted.

        Args:
            op: The call to run; it is retried only on a transient coded error.

        Returns:
            What the first successful attempt returns.

        """
        ...
