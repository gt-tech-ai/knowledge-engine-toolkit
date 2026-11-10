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

        Args:
            key: The cache key.

        Returns:
            The stored bytes, or ``None`` on a miss or an expired entry.

        """
        ...

    @abstractmethod
    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Set a value with a TTL in seconds.

        Args:
            key: The cache key.
            value: The bytes to store.
            ttl_seconds: How long the entry lives.

        """
        ...

    @abstractmethod
    async def delete(self, key: str) -> None:
        """Delete a key from the cache.

        Args:
            key: The cache key; a missing key is not an error.

        """
        ...

    @abstractmethod
    async def exists(self, key: str) -> bool:
        """Check if a key exists and is not expired.

        Args:
            key: The cache key.

        Returns:
            ``True`` when the key holds an unexpired entry.

        """
        ...


class CacheInvalidator(ABC):
    """Cache invalidation capabilities."""

    @abstractmethod
    async def invalidate_prefix(self, prefix: str) -> None:
        """Remove all keys matching the given prefix.

        Args:
            prefix: The key prefix whose entries are removed (for example one tenant's
                namespace).

        """
        ...

    @abstractmethod
    async def invalidate_all(self) -> None:
        """Clear the entire cache."""
        ...
