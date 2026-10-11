"""Unit tests for the NullCache no-op implementation.

Tests that NullCache always returns None on get, and that set/delete are no-ops.

# Running Tests

Run with: pytest tests/python/test_foundation/test_cache_null.py -v
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.cache.null import NullCache


class TestNullCache:
    """Test suite for no-op NullCache."""

    @pytest.mark.asyncio
    async def test_get_always_returns_none(self) -> None:
        """Test that get always returns None regardless of key.

        **Why this test is important:**
          - NullCache is used when caching is disabled; get must always miss
          - Returning anything other than None would violate the no-op contract

        **What it tests:**
          - result is None for any key
        """
        cache = NullCache()
        result = await cache.get("any-key")
        assert result is None

    @pytest.mark.asyncio
    async def test_set_is_noop(self) -> None:
        """Test that set does not store anything.

        **Why this test is important:**
          - NullCache must not accumulate memory from set calls
          - A subsequent get must still return None

        **What it tests:**
          - No exception from set, and get still returns None
        """
        cache = NullCache()
        await cache.set("key", b"value", ttl_seconds=60)
        result = await cache.get("key")
        assert result is None

    @pytest.mark.asyncio
    async def test_delete_is_noop(self) -> None:
        """Test that delete does not raise.

        **Why this test is important:**
          - Callers should be able to call delete without checking cache type
          - NullCache delete must be a silent no-op

        **What it tests:**
          - No exception raised on delete
        """
        cache = NullCache()
        # Should not raise
        await cache.delete("key")
