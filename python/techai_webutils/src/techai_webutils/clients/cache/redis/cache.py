"""Redis cache client with configurable failure modes.

Supports two failure modes:
- BYPASS (default): Redis client errors (redis-py's ``RedisError`` and socket errors)
  return a cache miss (None) on read and are logged on write, not raised
- ERROR: those errors raise a coded ``UnavailableError`` (cause chained)
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING, final, override

from redis.exceptions import RedisError

from techai_webutils.clients.cache.types import FailureMode
from techai_webutils.core.errors import UnavailableError
from techai_webutils.core.interfaces.cache import Cache

if TYPE_CHECKING:
    from redis.asyncio import Redis

logger = logging.getLogger(__name__)

_CLIENT_ERRORS = (RedisError, ConnectionError, OSError)
"""The failures a Redis call raises: redis-py's own (``RedisError``: its
``ConnectionError`` and ``TimeoutError`` subclass neither builtin) and the socket-level
builtins."""


@final
class RedisCache(Cache):
    """Async Redis cache wrapper with failure mode support."""

    def __init__(
        self, client: Redis, failure_mode: FailureMode = FailureMode.BYPASS
    ) -> None:
        """Initialize the Redis cache wrapper.

        Args:
            client: An async Redis client instance (``redis.asyncio.Redis``).
            failure_mode: How to handle connection failures. BYPASS returns
                cache miss on error; ERROR propagates the exception.

        """
        self._client = client
        self._failure_mode = failure_mode

    @override
    async def get(self, key: str) -> bytes | None:
        """Get a value from cache.

        Returns None on cache miss. A Redis failure returns None in BYPASS mode and
        raises ``UnavailableError`` in ERROR mode.

        Args:
            key: The cache key.

        Returns:
            A value from cache.

        """
        try:
            result = await self._client.get(key)
            if isinstance(result, str):
                return result.encode()
        except _CLIENT_ERRORS as e:
            self._absorb("get", key, e)
            return None
        else:
            return result

    @override
    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Set a value in cache with TTL.

        A Redis failure is logged in BYPASS mode and raises ``UnavailableError`` in
        ERROR mode.

        Args:
            key: The cache key.
            value: The bytes to store.
            ttl_seconds: How long the entry lives.

        """
        try:
            _ = await self._client.setex(key, ttl_seconds, value)
        except _CLIENT_ERRORS as e:
            self._absorb("set", key, e)

    @override
    async def delete(self, key: str) -> None:
        """Delete a key from cache.

        A Redis failure is logged in BYPASS mode and raises ``UnavailableError`` in
        ERROR mode.

        Args:
            key: The cache key.

        """
        try:
            _ = await self._client.delete(key)
        except _CLIENT_ERRORS as e:
            self._absorb("delete", key, e)

    @override
    async def exists(self, key: str) -> bool:
        """Check if a key exists in Redis.

        A Redis failure is logged in BYPASS mode and raises ``UnavailableError`` in
        ERROR mode.

        Args:
            key: The cache key.

        Returns:
            Whether the key exists.

        """
        try:
            return bool(await self._client.exists(key))
        except _CLIENT_ERRORS as e:
            self._absorb("exists", key, e)
            return False

    def _absorb(self, op: str, key: str, err: Exception) -> None:
        """Handle a failed Redis call by the configured failure mode.

        Args:
            op: The cache operation that failed.
            key: The key it was called with.
            err: The redis-py or socket error.

        Raises:
            UnavailableError: In ``FailureMode.ERROR``, with ``err`` as its cause.

        """
        if self._failure_mode == FailureMode.ERROR:
            msg = f"redis {op} failed"
            raise UnavailableError(msg) from err
        logger.warning(
            "redis %s failed (bypass mode)", op, extra={"key": key, "error": str(err)}
        )
