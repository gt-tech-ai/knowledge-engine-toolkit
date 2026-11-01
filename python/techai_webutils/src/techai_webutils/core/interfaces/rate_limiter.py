"""Rate limiter interfaces.

``RateLimiter`` mirrors Go's ``interfaces.RateLimiter`` (one global bucket, synchronous ``allow``).
``PerKeyRateLimiter`` counts per key (a user, an org) and is async, because a shared backend
(Redis) answers over the network.
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


class PerKeyRateLimiter(ABC):
    """Allows at most a configured number of events per key per window."""

    @abstractmethod
    async def allow_key(self, key: str) -> bool:
        """Count one event for ``key`` and report whether it is within the window's limit.

        A shared backend that can't answer raises a coded ``UNAVAILABLE``; the caller's policy
        decides whether that serves or rejects.
        """
        ...
