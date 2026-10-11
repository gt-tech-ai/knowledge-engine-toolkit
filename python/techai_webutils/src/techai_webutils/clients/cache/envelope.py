"""Cache envelope for typed, version-aware caching.

Provides a generic CacheEnvelope[T] that wraps cached values with schema
version metadata. When the envelope's version doesn't match the expected
version, the cached entry is treated as a miss.
"""

from __future__ import annotations

import json
import time
from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class CacheEnvelope[T]:
    """Wraps a cached value with metadata for version-aware caching."""

    data: T
    """The cached value being wrapped."""
    cached_at: float
    """Unix timestamp (seconds) when the value was cached."""
    version: int
    """Schema version of the cached value; a mismatch on read is treated as a miss."""


def encode(value: object, version: int) -> bytes:
    """Serialize value into a versioned envelope as JSON bytes."""
    envelope = {
        "data": value,
        "cached_at": time.time(),
        "version": version,
    }
    return json.dumps(envelope).encode("utf-8")


def decode(data: bytes, version: int) -> tuple[object | None, bool]:
    """Deserialize bytes into a versioned envelope.

    Returns ``(value, True)`` on success and ``(None, False)`` on version
    mismatch. Raises on corrupt/invalid JSON (distinct from version mismatch).

    Args:
        data: JSON bytes produced by :func:`encode`.
        version: Expected schema version. Mismatch → ``(None, False)``.

    """
    envelope = json.loads(data)
    if envelope["version"] != version:
        return None, False
    return envelope["data"], True
