"""No-op cache implementation.

Always returns None on get; set and delete are silent no-ops.
Useful when caching is disabled or in testing scenarios.
"""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.cache import Cache


class NullCache(Cache):
    """No-op cache that never stores anything."""

    @override
    async def get(self, key: str) -> bytes | None:
        """Return None (cache miss) for every key."""
        return None

    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """No-op."""

    async def delete(self, key: str) -> None:
        """No-op."""

    @override
    async def exists(self, key: str) -> bool:
        """Return False (key never exists) for every key."""
        return False
