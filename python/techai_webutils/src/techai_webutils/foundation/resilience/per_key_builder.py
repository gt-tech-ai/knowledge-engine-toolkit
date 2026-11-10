"""Config-selected ``PerKeyRateLimiter`` factory (``memory`` or ``redis``)."""

from __future__ import annotations

import importlib
import time
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.foundation.resilience.per_key_rate_limiter import InMemoryRateLimiter

if TYPE_CHECKING:
    from collections.abc import Callable

    from redis.asyncio import Redis

    from techai_webutils.core.interfaces.rate_limiter import PerKeyRateLimiter
    from techai_webutils.foundation.resilience.redis_rate_limiter import (
        RedisPerKeyRateLimiter as _LazyRedisPerKeyRateLimiter,
    )


class PerKeyRateLimiterKind(StrEnum):
    """The per-key rate limiter backends."""

    MEMORY = "memory"
    """Process-local (each replica counts alone; the default)."""
    REDIS = "redis"
    """Shared across replicas through Redis."""


@dataclass(frozen=True, slots=True)
class PerKeyRateLimiterConfig:
    """Per-key rate limiter configuration."""

    kind: str = PerKeyRateLimiterKind.MEMORY
    """A ``PerKeyRateLimiterKind`` value."""
    max_per_window: int = 0
    """Events allowed per key per window; ``0`` is unlimited (the fail-safe default)."""
    window_seconds: int = 60
    """The window length in seconds."""
    prefix: str = "ratelimit"
    """Key prefix of the Redis counters (``<prefix>:<key>:<window index>``)."""


def per_key_rate_limiter_from_config(
    config: PerKeyRateLimiterConfig,
    *,
    redis: Redis | None = None,
    clock: Callable[[], float] = time.time,
) -> PerKeyRateLimiter:
    """Build the per-key limiter ``config.kind`` selects.

    Args:
        config: The limiter configuration.
        redis: The async Redis client (required by ``redis``).
        clock: Epoch seconds the ``redis`` window index is computed from (injected for
            tests).

    Returns:
        The per-key limiter ``config.kind`` selects.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``redis`` without a client.

    """
    if config.kind == PerKeyRateLimiterKind.MEMORY:
        return InMemoryRateLimiter(
            config.max_per_window, window_seconds=config.window_seconds
        )
    if config.kind == PerKeyRateLimiterKind.REDIS:
        if redis is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "redis rate limiter requires a client"
            )
        redis_per_key_rate_limiter_cls = cast(
            "type[_LazyRedisPerKeyRateLimiter]",
            importlib.import_module(
                "techai_webutils.foundation.resilience.redis_rate_limiter"
            ).RedisPerKeyRateLimiter,
        )

        return redis_per_key_rate_limiter_cls(
            redis,
            max_per_window=config.max_per_window,
            window_seconds=config.window_seconds,
            prefix=config.prefix,
            clock=clock,
        )
    msg = f"unknown per-key rate limiter kind: {config.kind!r}"
    raise AppValueError(msg)
