"""Rate limiter interface.

Mirrors Go's ``interfaces.RateLimiter``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class RateLimiter(ABC):
    """Controls the rate of operations using a token bucket algorithm."""

    @abstractmethod
    def allow(self) -> bool:
        """Report whether an event may happen now (non-blocking)."""
        ...

    @abstractmethod
    async def wait(self) -> None:
        """Block until the limiter permits an event."""
        ...
