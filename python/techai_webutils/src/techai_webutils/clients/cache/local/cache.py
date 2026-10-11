"""In-memory cache with TTL-based expiration.

Stores key-value pairs in a dictionary with monotonic clock expiry timestamps.
Expired entries are lazily evicted on get. Suitable for development and testing;
not shared across processes.
"""

from __future__ import annotations

import asyncio
import time

from techai_webutils.core.interfaces.cache import Cache


class LocalCache(Cache):
    """Async in-memory cache with per-key TTL expiration.

    Args:
        default_ttl: Default TTL in seconds for keys stored without an explicit TTL.

    """

    def __init__(self, default_ttl: int = 300) -> None:
        """Create an empty in-memory store guarded by an asyncio lock.

        Args:
            default_ttl: TTL in seconds applied when a value is set without an
                explicit (positive) TTL.

        """
        self._default_ttl = default_ttl
        self._store: dict[str, tuple[bytes, float]] = {}
        self._lock = asyncio.Lock()

    async def get(self, key: str) -> bytes | None:
        """Get a value by key. Returns None on miss or if the key has expired."""
        async with self._lock:
            entry = self._store.get(key)
            if entry is None:
                return None
            value, expiry = entry
            if time.monotonic() >= expiry:
                del self._store[key]
                return None
            return value

    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Set a value with a TTL in seconds. Uses default_ttl if ttl_seconds is 0."""
        ttl = ttl_seconds if ttl_seconds > 0 else self._default_ttl
        expiry = time.monotonic() + ttl
        async with self._lock:
            self._store[key] = (value, expiry)

    async def delete(self, key: str) -> None:
        """Delete a key from the cache. No-op if the key does not exist."""
        async with self._lock:
            self._store.pop(key, None)

    async def exists(self, key: str) -> bool:
        """Check if a key exists and is not expired."""
        async with self._lock:
            entry = self._store.get(key)
            if entry is None:
                return False
            _, expiry = entry
            if time.monotonic() >= expiry:
                del self._store[key]
                return False
            return True
