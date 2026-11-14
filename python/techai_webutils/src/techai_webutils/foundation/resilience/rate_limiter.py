"""Token bucket rate limiter.

Implements the ``RateLimiter`` ABC from core/interfaces using a simple
token bucket algorithm with ``asyncio.Lock`` for thread safety and
timed token refill. ``with_recorder`` counts each refused ``allow`` as a
``rate_limited`` rejection.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Self, final, override

from techai_webutils.core.interfaces.rate_limiter import RateLimiter
from techai_webutils.foundation.resilience.observer import UNOBSERVED
from techai_webutils.foundation.resilience.series import Reason

if TYPE_CHECKING:
    from techai_webutils.foundation.resilience.series import Recorder


@final
class TokenBucketRateLimiter(RateLimiter):
    """Token bucket rate limiter."""

    def __init__(self, rate: float, burst: int) -> None:
        """Start the bucket full (``burst`` tokens) with the refill clock primed.

        Args:
            rate: Sustained token refill rate in tokens per second.
            burst: Bucket capacity — the largest momentary burst permitted.

        """
        self._rate = rate
        self._burst = burst
        self._tokens = float(burst)
        self._last_refill = time.monotonic()
        self._lock = asyncio.Lock()
        self._recorder: Recorder = UNOBSERVED.recorder

    def with_recorder(self, recorder: Recorder) -> Self:
        """Count each refusal as a ``rate_limited`` rejection (operation "").

        Args:
            recorder: Where the refusals are counted.

        Returns:
            This limiter.

        """
        self._recorder = recorder
        return self

    def _refill(self) -> None:
        """Add tokens based on elapsed time since last refill."""
        now = time.monotonic()
        elapsed = now - self._last_refill
        self._tokens = min(self._burst, self._tokens + elapsed * self._rate)
        self._last_refill = now

    @override
    def allow(self) -> bool:
        """Report whether an event may happen now (non-blocking).

        Returns:
            Whether the event may happen now.

        """
        self._refill()
        if self._tokens >= 1.0:
            self._tokens -= 1.0
            return True
        self._recorder.rejected("", Reason.RATE_LIMITED)
        return False

    @override
    async def wait(self) -> None:
        """Block until the limiter permits an event."""
        while True:
            async with self._lock:
                self._refill()
                if self._tokens >= 1.0:
                    self._tokens -= 1.0
                    return
                # Calculate wait time for one token
                wait_time = (1.0 - self._tokens) / self._rate
            await asyncio.sleep(wait_time)
