"""In-memory TTL deduplicator.

The default ``Deduplicator`` backend: a process-local map of ``key -> first-seen time``.
Mirrors Go's ``foundation/resilience/dedup.Memory``. For cross-replica dedup, a shared
store (Redis/Postgres) implements the same ``Deduplicator`` Protocol. ``with_recorder``
counts each duplicate, as Go's ``Memory.WithRecorder`` does.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Self

from techai_webutils.foundation.resilience.observer import UNOBSERVED

if TYPE_CHECKING:
    from techai_webutils.foundation.resilience.series import Recorder


class MemoryDeduplicator:
    """A process-local ``Deduplicator`` backed by a TTL map.

    A key first ``seen`` is recorded and reported not-a-duplicate; every repeat within the
    TTL is a duplicate. ``ttl_seconds`` of 0 (the default) remembers forever (never
    evicts). Not shared across replicas -- use a Redis/Postgres backend for cross-pod
    dedup.
    """

    def __init__(self, ttl_seconds: float = 0.0) -> None:
        """Create the store; ``ttl_seconds`` <= 0 remembers keys forever.

        Args:
            ttl_seconds: How long a key is remembered; 0 or less is forever.

        """
        self._ttl: float = ttl_seconds
        self._seen: dict[str, float] = {}
        self._lock: asyncio.Lock = asyncio.Lock()
        self._recorder: Recorder = UNOBSERVED.recorder

    def with_recorder(self, recorder: Recorder) -> Self:
        """Count each duplicate ``seen`` reports.

        Args:
            recorder: Where the duplicates are counted.

        Returns:
            This deduplicator.

        """
        self._recorder = recorder
        return self

    async def seen(self, key: str) -> bool:
        """Return True if ``key`` is a live (un-expired) repeat.

        Otherwise record it and return False.

        Args:
            key: The work item's identity (for example a message id).

        Returns:
            True if ``key`` is a live (un-expired) repeat.

        """
        now = time.monotonic()
        async with self._lock:
            first = self._seen.get(key)
            if first is not None and (self._ttl <= 0 or now - first < self._ttl):
                self._recorder.deduplicated()
                return True
            self._seen[key] = now
            return False
