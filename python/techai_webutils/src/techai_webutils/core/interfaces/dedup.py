"""Deduplication interface for at-least-once delivery idempotency.

Mirrors Go's ``interfaces.Deduplicator``. A ``Deduplicator`` records the keys it has seen
so a redelivered message is processed exactly once.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable


@runtime_checkable
class Deduplicator(Protocol):
    """Provides idempotency for at-least-once delivery.

    Implementations: an in-memory TTL store (default), or a shared store (Redis/Postgres)
    for cross-replica dedup. Handler code depends on this Protocol, never a concrete store.
    """

    async def seen(self, key: str) -> bool:
        """Atomically report whether ``key`` was already processed, marking it if not.

        Returns True when ``key`` is a duplicate (the caller should skip it) and False on the
        first sighting (the caller should process it).
        """
        ...
