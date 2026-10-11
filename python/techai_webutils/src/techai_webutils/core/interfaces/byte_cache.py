"""Byte cache interface for raw byte storage.

Mirrors Go's ``interfaces.ByteCache``. Used by infrastructure wrappers
(metrics, tracing) that operate on serialized values without caring
about the concrete type.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class ByteCache(ABC):
    """Non-generic cache for raw byte storage.

    Compared to the async ``Cache`` ABC, ByteCache:
      - Uses ``bytes`` values instead of typed values
      - Returns ``(bytes | None, bool)`` from get
      - ``set`` does not return an error (fire-and-forget)
    """

    @abstractmethod
    def get(self, key: str) -> tuple[bytes | None, bool]:
        """Retrieve a value by key. Returns ``(None, False)`` if not found."""
        ...

    @abstractmethod
    def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Store a value with the given TTL in seconds."""
        ...

    @abstractmethod
    def delete(self, key: str) -> None:
        """Remove a value by key."""
        ...
