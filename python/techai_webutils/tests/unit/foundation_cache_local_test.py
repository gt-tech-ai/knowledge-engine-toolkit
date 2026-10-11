"""Unit tests for the LocalCache in-memory implementation.

Tests that LocalCache correctly stores, retrieves, and expires key-value pairs
using an in-memory dictionary with TTL-based expiration.

# Test Coverage

The tests cover:
  - get returns stored value before TTL expires
  - get returns None after TTL expires
  - set overwrites existing values
  - delete removes a stored key
  - delete on missing key is a no-op
  - default TTL is applied when no per-key TTL is given

# Running Tests

Run with: pytest tests/python/test_foundation/test_cache_local.py -v
"""

from __future__ import annotations

import time
from unittest.mock import patch

import pytest

from techai_webutils.clients.cache.local import LocalCache


class TestLocalCache:
    """Test suite for in-memory LocalCache with TTL expiration."""

    @pytest.mark.asyncio
    async def test_get_returns_stored_value(self) -> None:
        """Test that get returns a previously stored value.

        **Why this test is important:**
          - The fundamental cache contract: set then get must return the value
          - Ensures the in-memory storage correctly associates keys with values

        **What it tests:**
          - result equals the bytes value passed to set
        """
        cache = LocalCache(default_ttl=300)
        await cache.set("key1", b"value1")

        result = await cache.get("key1")
        assert result == b"value1"

    @pytest.mark.asyncio
    async def test_get_returns_none_on_miss(self) -> None:
        """Test that get returns None for a key that was never stored.

        **Why this test is important:**
          - Cache misses must be distinguishable from hits for fallthrough logic
          - Returning anything other than None on miss would corrupt caller logic

        **What it tests:**
          - result is None for a non-existent key
        """
        cache = LocalCache(default_ttl=300)

        result = await cache.get("missing")
        assert result is None

    @pytest.mark.asyncio
    async def test_get_returns_none_after_ttl_expires(self) -> None:
        """Test that get returns None after the TTL has expired.

        **Why this test is important:**
          - TTL expiration prevents serving stale data
          - Without expiry, the in-memory cache would grow unbounded

        **What it tests:**
          - result is None after the TTL window has passed (using mocked time)
        """
        cache = LocalCache(default_ttl=300)
        await cache.set("key1", b"value1", ttl_seconds=1)

        # Mock time to simulate TTL expiry
        with patch("techai_webutils.clients.cache.local.cache.time") as mock_time:
            mock_time.monotonic.return_value = time.monotonic() + 2
            result = await cache.get("key1")

        assert result is None

    @pytest.mark.asyncio
    async def test_set_overwrites_existing_value(self) -> None:
        """Test that set replaces a previously stored value.

        **Why this test is important:**
          - Cache updates must replace stale entries, not append
          - Document updates require cache invalidation via overwrite

        **What it tests:**
          - result equals the new value after overwriting
        """
        cache = LocalCache(default_ttl=300)
        await cache.set("key1", b"old")
        await cache.set("key1", b"new")

        result = await cache.get("key1")
        assert result == b"new"

    @pytest.mark.asyncio
    async def test_delete_removes_key(self) -> None:
        """Test that delete removes a stored key.

        **Why this test is important:**
          - Cache invalidation after data mutations must work correctly
          - Stale cached data after deletion would cause data consistency issues

        **What it tests:**
          - result is None after deleting a previously stored key
        """
        cache = LocalCache(default_ttl=300)
        await cache.set("key1", b"value1")
        await cache.delete("key1")

        result = await cache.get("key1")
        assert result is None

    @pytest.mark.asyncio
    async def test_delete_missing_key_is_noop(self) -> None:
        """Test that deleting a non-existent key does not raise.

        **Why this test is important:**
          - Callers should not need to check existence before deletion
          - Double-delete scenarios (retry logic) must not crash

        **What it tests:**
          - No exception raised when deleting a non-existent key
        """
        cache = LocalCache(default_ttl=300)
        # Should not raise
        await cache.delete("missing")

    @pytest.mark.asyncio
    async def test_default_ttl_is_applied(self) -> None:
        """Test that the default TTL is used when ttl_seconds=0 is passed.

        **Why this test is important:**
          - Callers who pass ttl_seconds=0 should get the instance's default_ttl
          - The default must actually be applied, not silently ignored

        **What it tests:**
          - Value is retrievable immediately (within default TTL window)
          - Value expires after default TTL passes (using mocked time)
        """
        cache = LocalCache(default_ttl=10)
        await cache.set("key1", b"value1", ttl_seconds=0)

        # Should be available immediately
        result = await cache.get("key1")
        assert result == b"value1"

        # Should expire after default TTL
        with patch("techai_webutils.clients.cache.local.cache.time") as mock_time:
            mock_time.monotonic.return_value = time.monotonic() + 11
            result = await cache.get("key1")
        assert result is None
