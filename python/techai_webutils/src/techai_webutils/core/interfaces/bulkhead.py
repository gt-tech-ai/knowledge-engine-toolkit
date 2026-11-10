"""Bulkhead interface for concurrency limiting.

Mirrors Go's ``interfaces.Bulkhead``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


class Bulkhead(ABC):
    """Limits concurrent access to a resource."""

    @abstractmethod
    async def execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run fn within the concurrency limit.

        Blocks until a slot is available.

        Args:
            fn: The call to run once a slot is free.

        Returns:
            What ``fn`` returns.

        """
        ...

    @abstractmethod
    async def try_execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Attempt to run fn without blocking.

        Raises ``BulkheadFullError`` if no slots are available.

        Args:
            fn: The call to run if a slot is free now.

        Returns:
            What ``fn`` returns.

        """
        ...
