"""Redis cache client with configurable failure modes.

Supports two failure modes:
- BYPASS (default): Connection errors return cache miss (None), not exceptions
- ERROR: Connection errors propagate as exceptions
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

from techai_webutils.clients.cache.types import FailureMode
from techai_webutils.core.interfaces.cache import Cache

if TYPE_CHECKING:
    from redis.asyncio import Redis

logger = logging.getLogger(__name__)


class RedisCache(Cache):
    """Async Redis cache wrapper with failure mode support.

    Args:
        client: An async Redis client (redis.asyncio.Redis).
        failure_mode: How to handle connection failures.

    """

    def __init__(
        self, client: Redis, failure_mode: FailureMode = FailureMode.BYPASS
    ) -> None:  # type: ignore[type-arg]
        """Initialize the Redis cache wrapper.

        Args:
            client: An async Redis client instance (``redis.asyncio.Redis``).
            failure_mode: How to handle connection failures. BYPASS returns
                cache miss on error; ERROR propagates the exception.

        """
        self._client = client
        self._failure_mode = failure_mode

    async def get(self, key: str) -> bytes | None:
        """Get a value from cache.

        Returns None on cache miss. In BYPASS mode, also returns None on connection error.
        """
        try:
            result = await self._client.get(key)
            if isinstance(result, str):
                return result.encode()
            return result
        except (ConnectionError, OSError) as e:
            if self._failure_mode == FailureMode.ERROR:
                raise
            logger.warning(
                "redis get failed (bypass mode)", extra={"key": key, "error": str(e)}
            )
            return None

    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Set a value in cache with TTL."""
        try:
            await self._client.setex(key, ttl_seconds, value)
        except (ConnectionError, OSError) as e:
            if self._failure_mode == FailureMode.ERROR:
                raise
            logger.warning(
                "redis set failed (bypass mode)", extra={"key": key, "error": str(e)}
            )

    async def delete(self, key: str) -> None:
        """Delete a key from cache."""
        try:
            await self._client.delete(key)
        except (ConnectionError, OSError) as e:
            if self._failure_mode == FailureMode.ERROR:
                raise
            logger.warning(
                "redis delete failed (bypass mode)", extra={"key": key, "error": str(e)}
            )

    async def exists(self, key: str) -> bool:
        """Check if a key exists in Redis."""
        try:
            return bool(await self._client.exists(key))
        except (ConnectionError, OSError) as e:
            if self._failure_mode == FailureMode.ERROR:
                raise
            logger.warning(
                "redis exists failed (bypass mode)", extra={"key": key, "error": str(e)}
            )
            return False
