"""Unit tests for the Redis fixed-window per-key rate limiter.

And the per-key limiter factory.
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.foundation.resilience.per_key_builder import (
    PerKeyRateLimiterConfig,
    PerKeyRateLimiterKind,
    per_key_rate_limiter_from_config,
)
from techai_webutils.foundation.resilience.per_key_rate_limiter import InMemoryRateLimiter


def _redis(counts: list[int] | Exception) -> MagicMock:
    """Return a Redis client whose pipeline's ``INCR`` answers ``counts`` in turn.

    (or raises).

    Returns:
        A Redis client whose pipeline's ``INCR`` answers ``counts`` in turn.

    """
    pipe = MagicMock()
    pipe.__aenter__ = AsyncMock(return_value=pipe)
    pipe.__aexit__ = AsyncMock(return_value=False)
    if isinstance(counts, Exception):
        pipe.execute = AsyncMock(side_effect=counts)
    else:
        pipe.execute = AsyncMock(side_effect=[[count, True] for count in counts])
    redis = MagicMock()
    redis.pipeline = MagicMock(return_value=pipe)
    return redis


@pytest.mark.asyncio
async def test_redis_rate_limiter_rejects_over_window() -> None:
    """Test that the Redis limiter allows up to the limit in a window, then rejects.

    **Why this test is important:**
      - The limiter is the abuse guard shared by every replica; an off-by-one lets a burst
        through.

    **What it tests:**
      - with a limit of 2, the window's 1st and 2nd calls are allowed and the 3rd rejected
      - the counter key carries the prefix, the caller's key and the window index
      - a Redis error raises ``UNAVAILABLE`` (the caller's policy decides open or closed)
    """
    redis = _redis([1, 2, 3])
    limiter = per_key_rate_limiter_from_config(
        PerKeyRateLimiterConfig(
            kind=PerKeyRateLimiterKind.REDIS,
            max_per_window=2,
            window_seconds=60,
            prefix="rl",
        ),
        redis=redis,
        clock=lambda: 125.0,
    )

    results = [await limiter.allow_key("u-1") for _ in range(3)]
    broken = per_key_rate_limiter_from_config(
        PerKeyRateLimiterConfig(kind=PerKeyRateLimiterKind.REDIS, max_per_window=2),
        redis=_redis(ConnectionError("down")),
    )
    with pytest.raises(AppError) as caught:
        await broken.allow_key("u-1")

    assert results == [True, True, False]
    redis.pipeline.return_value.incr.assert_called_with("rl:u-1:2")
    redis.pipeline.return_value.expire.assert_called_with("rl:u-1:2", 60)
    assert caught.value.code is ErrorCode.UNAVAILABLE


@pytest.mark.asyncio
async def test_zero_limit_is_unlimited() -> None:
    """Test that a zero limit allows every call without touching Redis.

    **Why this test is important:**
      - ``0 = unlimited`` is the fail-safe base config; it must never reject or cost a
        round trip.

    **What it tests:**
      - ``max_per_window=0`` allows for both kinds; the Redis pipeline is never opened
    """
    redis = _redis([])
    for kind in PerKeyRateLimiterKind:
        limiter = per_key_rate_limiter_from_config(
            PerKeyRateLimiterConfig(kind=kind, max_per_window=0), redis=redis
        )
        assert all([await limiter.allow_key("u-1") for _ in range(5)])
    redis.pipeline.assert_not_called()


def test_unknown_kind_raises() -> None:
    """Test that an unknown kind, or ``redis`` without a client, fails at boot.

    **Why this test is important:**
      - A typo in config must fail loudly, not silently fall back to a per-replica
        limiter.

    **What it tests:**
      - an unknown kind raises ``INVALID_INPUT``; so does ``redis`` with no client
      - the default kind is the in-memory limiter
    """
    with pytest.raises(AppError) as unknown:
        per_key_rate_limiter_from_config(PerKeyRateLimiterConfig(kind="memcached"))
    with pytest.raises(AppError) as no_client:
        per_key_rate_limiter_from_config(
            PerKeyRateLimiterConfig(kind=PerKeyRateLimiterKind.REDIS)
        )

    assert unknown.value.code is ErrorCode.INVALID_INPUT
    assert no_client.value.code is ErrorCode.INVALID_INPUT
    assert isinstance(
        per_key_rate_limiter_from_config(PerKeyRateLimiterConfig()), InMemoryRateLimiter
    )
