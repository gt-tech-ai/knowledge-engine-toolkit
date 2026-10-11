"""Cache builder with kind enum, config dataclass, and factory function.

Creates cache instances based on configuration. Supports REDIS, LOCAL, and NULL
cache kinds. The factory function routes to the correct implementation based on
the CacheKind in the config.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.cache.local import LocalCache
from techai_webutils.clients.cache.null import NullCache
from techai_webutils.clients.cache.redis import RedisCache
from techai_webutils.clients.cache.types import FailureMode
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.cache import Cache


class CacheKind(StrEnum):
    """Available cache implementations."""

    REDIS = "redis"
    """The Redis-backed cache (shared, cross-pod; stage/prod)."""
    LOCAL = "local"
    """The in-process TTL cache (single-pod; dev/test)."""
    NULL = "null"
    """The no-op cache that never stores or returns a value (caching disabled)."""


@dataclass
class CacheConfig:
    """Configuration for cache creation.

    Attributes:
        kind: Which cache implementation to use.
        default_ttl: Default TTL in seconds.
        redis_client: An async Redis client instance (required when kind is REDIS).
        failure_mode: Redis failure mode (only used when kind is REDIS).

    """

    kind: CacheKind = CacheKind.LOCAL
    """Selects the cache implementation (defaults to the in-process LOCAL cache)."""
    default_ttl: int = 300
    """Default time-to-live for cached entries, in seconds."""
    redis_client: Any = None
    """An async Redis client instance (required when ``kind`` is REDIS)."""
    failure_mode: FailureMode = FailureMode.BYPASS
    """How a Redis failure is handled (only used when ``kind`` is REDIS)."""


def default_config() -> CacheConfig:
    """Return a default cache config (LOCAL with 300s TTL)."""
    return CacheConfig(kind=CacheKind.LOCAL, default_ttl=300)


def new_cache_from_config(config: CacheConfig) -> Cache:
    """Create a cache instance from config.

    Args:
        config: Cache configuration specifying kind and parameters.

    Returns:
        A Cache implementation matching the requested kind.

    Raises:
        ValueError: If the kind is unknown or required parameters are missing.

    """
    if config.kind == CacheKind.LOCAL:
        return LocalCache(default_ttl=config.default_ttl)

    if config.kind == CacheKind.NULL:
        return NullCache()

    if config.kind == CacheKind.REDIS:
        if config.redis_client is None:
            msg = "redis_client is required when kind is REDIS"
            raise AppValueError(msg)
        return RedisCache(client=config.redis_client, failure_mode=config.failure_mode)

    msg = f"unknown cache kind: {config.kind}"
    raise AppValueError(msg)
