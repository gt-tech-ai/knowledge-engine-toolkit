"""Tests for the deduplication + leader-election resilience primitives.

Parity with Go's ``go/tests/unit/foundation_dedup_leader_test.go``: the in-memory deduplicator
gives exactly-once semantics under redelivery and the single-process leader always holds
leadership.
"""

from __future__ import annotations

import asyncio

import pytest

from techai_webutils.foundation.resilience.dedup import MemoryDeduplicator
from techai_webutils.foundation.resilience.leader import AlwaysLeader


@pytest.mark.asyncio
async def test_memory_deduplicator_first_seen_then_duplicate() -> None:
    """Exactly-once: a key is new on first sighting, a duplicate on repeat, keys independent.

    Why this test is important:
        - Under at-least-once delivery a redelivered message must be processed once; the
          deduplicator is the guard, and a first-seen key wrongly reported as a duplicate would
          drop a real message.

    What it tests:
        - First ``seen(k)`` is False (process it), the repeat is True (skip it), and a different
          key is independently first-seen.
    """
    dedup = MemoryDeduplicator()  # remember forever

    assert await dedup.seen("k1") is False, "first sighting must not be a duplicate"
    assert await dedup.seen("k1") is True, "a repeat sighting must be a duplicate"
    assert await dedup.seen("k2") is False, "a distinct key is tracked independently"


@pytest.mark.asyncio
async def test_memory_deduplicator_ttl_expiry_reprocesses() -> None:
    """A key seen longer ago than the TTL is treated as new again (bounded memory).

    Why this test is important:
        - A finite-TTL store must forget old keys so its memory is bounded and a legitimately
          re-sent message after the window is reprocessed rather than silently dropped forever.

    What it tests:
        - With a tiny TTL, a key seen once is a duplicate immediately but new again after the
          window elapses.
    """
    dedup = MemoryDeduplicator(ttl_seconds=0.05)

    assert await dedup.seen("k") is False
    assert await dedup.seen("k") is True, "within the TTL it is a duplicate"

    await asyncio.sleep(0.06)
    assert await dedup.seen("k") is False, "past the TTL the key is new again"


@pytest.mark.asyncio
async def test_always_leader_is_leader() -> None:
    """The single-process elector always reports leadership, so a guarded job runs in dev.

    Why this test is important:
        - The single-process default must never withhold leadership, or the guarded job would
          never run on a single-replica deployment.

    What it tests:
        - ``AlwaysLeader.is_leader`` returns True.
    """
    assert await AlwaysLeader().is_leader() is True
