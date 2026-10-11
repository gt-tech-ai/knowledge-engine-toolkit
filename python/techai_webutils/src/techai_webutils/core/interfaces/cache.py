"""Cache interface for key-value storage.

Defines the abstract contract for cache implementations (Redis, in-memory, etc.).
Foundation implementations satisfy this interface; consumers depend only on it.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class Cache(ABC):
    """Abstract cache for async key-value storage.

    Implementations: RedisCache (default), in-memory, null/no-op.
    All service code depends on this interface, never on a concrete cache.
    """

    @abstractmethod
    async def get(self, key: str) -> bytes | None:
        """Get a value by key.

        Returns None on cache miss or if the key has expired.
        """
        ...

    @abstractmethod
    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Set a value with a TTL in seconds."""
        ...

    @abstractmethod
    async def delete(self, key: str) -> None:
        """Delete a key from the cache."""
        ...

    @abstractmethod
    async def exists(self, key: str) -> bool:
        """Check if a key exists and is not expired."""
        ...


class CacheInvalidator(ABC):
    """Cache invalidation capabilities."""

    @abstractmethod
    async def invalidate_prefix(self, prefix: str) -> None:
        """Remove all keys matching the given prefix."""
        ...

    @abstractmethod
    async def invalidate_all(self) -> None:
        """Clear the entire cache."""
        ...
