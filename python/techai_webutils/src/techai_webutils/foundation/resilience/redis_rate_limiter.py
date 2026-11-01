"""``RedisPerKeyRateLimiter`` — a fixed-window per-key limit shared by every replica.

Each call runs ``INCR <prefix>:<key>:<window index>`` and ``EXPIRE … window_seconds`` in one
pipeline; the call is allowed while the count is within ``max_per_window``. The window index is
``floor(clock() / window_seconds)``, so every replica agrees on the window without coordination.
``max_per_window == 0`` is unlimited and never touches Redis. A Redis error raises a coded
``UNAVAILABLE``: the caller's policy decides whether that serves or rejects.
"""

from __future__ import annotations

import time
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.rate_limiter import PerKeyRateLimiter

if TYPE_CHECKING:
    from collections.abc import Callable

    from redis.asyncio import Redis


class RedisPerKeyRateLimiter(PerKeyRateLimiter):
    """A fixed-window per-key rate limiter on a shared Redis."""

    def __init__(
        self,
        redis: Redis,
        *,
        max_per_window: int,
        window_seconds: int = 60,
        prefix: str = "ratelimit",
        clock: Callable[[], float] = time.time,
    ) -> None:
        """Bind the client and the window (``max_per_window`` 0: unlimited; ``clock``: epoch seconds)."""
        self._redis = redis
        self._max = max_per_window
        self._window = window_seconds
        self._prefix = prefix
        self._clock = clock

    @override
    async def allow_key(self, key: str) -> bool:
        """Count one event for ``key`` in the current window; True while within the limit.

        Raises:
            AppError: ``UNAVAILABLE`` when Redis can't answer.

        """
        if self._max == 0:
            return True
        counter = f"{self._prefix}:{key}:{int(self._clock() // self._window)}"
        try:
            async with self._redis.pipeline(transaction=True) as pipe:
                pipe.incr(counter)
                pipe.expire(counter, self._window)
                count, _ = await pipe.execute()
        except Exception as exc:
            msg = "rate limiter unavailable"
            raise AppError(ErrorCode.UNAVAILABLE, msg, cause=exc) from exc
        return int(count) <= self._max
