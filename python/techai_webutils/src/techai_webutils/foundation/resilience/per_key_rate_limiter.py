"""Process-local per-key fixed-window rate limiter (a generic send/throttle guard).

A fixed-window counter per key: at most ``max_per_window`` allowances per ``window_seconds`` (default
one hour). Process-local (per replica) — a shared/distributed limiter is a future enhancement. An
injected ``clock`` (default ``time.monotonic``) keeps the window behaviour unit-testable. Distinct from
the peer ``TokenBucketRateLimiter``, which is a single global bucket (no per-key keying).
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Callable


# Purge fully-expired keys once the map grows past this many entries. Without this the map retains one
# entry per distinct key ever seen (inactive users' windows are never revisited), leaking memory in a
# long-running worker. The sweep is O(n) but amortized — it runs only when the map exceeds the cap.
_EVICT_THRESHOLD = 10_000
"""Key-map size past which fully-expired keys are purged to bound worker memory."""


class InMemoryRateLimiter:
    """A per-key fixed-window allowance counter (``allow`` returns False once the window is exhausted)."""

    def __init__(
        self,
        max_per_window: int,
        *,
        window_seconds: float = 3600.0,
        clock: Callable[[], float] = time.monotonic,
        evict_threshold: int = _EVICT_THRESHOLD,
    ) -> None:
        """Allow at most ``max_per_window`` per ``window_seconds`` per key; ``clock`` is the time source.

        ``evict_threshold`` is the map size past which fully-expired keys are purged
        (default bounds a long-running worker's memory; injectable so a test can lower
        it to exercise the eviction path without adding thousands of keys).
        """
        self._max: int = max_per_window
        self._window: float = window_seconds
        self._clock: Callable[[], float] = clock
        self._evict_threshold: int = evict_threshold
        # key -> (window_start, count in the current window).
        self._counts: dict[str, tuple[float, int]] = {}
        self._lock: asyncio.Lock = asyncio.Lock()

    async def allow(self, key: str) -> bool:
        """Return True and count one allowance if ``key`` is under its window limit, else False."""
        now = self._clock()
        async with self._lock:
            if len(self._counts) >= self._evict_threshold:
                self._evict_expired(now)
            window_start, count = self._counts.get(key, (now, 0))
            if now - window_start >= self._window:
                window_start, count = now, 0
            if count >= self._max:
                self._counts[key] = (window_start, count)
                return False
            self._counts[key] = (window_start, count + 1)
            return True

    def _evict_expired(self, now: float) -> None:
        """Drop keys whose window fully elapsed (under the lock); bounds size to keys active in a window."""
        expired = [
            k
            for k, (window_start, _) in self._counts.items()
            if now - window_start >= self._window
        ]
        for k in expired:
            del self._counts[k]
