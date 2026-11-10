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
        """Return None (cache miss) for every key.

        Args:
            key: Ignored.

        Returns:
            None (cache miss) for every key.

        """
        return None

    @override
    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """No-op.

        Args:
            key: The cache key.
            value: Ignored.
            ttl_seconds: How long the entry lives.

        """

    @override
    async def delete(self, key: str) -> None:
        """No-op.

        Args:
            key: Ignored.

        """

    @override
    async def exists(self, key: str) -> bool:
        """Return False (key never exists) for every key.

        Args:
            key: Ignored.

        Returns:
            False (key never exists) for every key.

        """
        return False
