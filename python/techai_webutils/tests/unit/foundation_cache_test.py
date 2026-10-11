"""Unit tests for the foundation cache module.

This file tests that the Redis cache abstraction correctly handles cache hits,
misses, TTL-based storage, key deletion, and failure mode behavior (bypass vs
error propagation) when the Redis connection is unavailable.

# Test Coverage

The tests cover:
  - Cache hit: returns stored value on get
  - Cache miss: returns None when key does not exist
  - Bypass mode: connection errors return None (graceful degradation)
  - Error mode: connection errors propagate to caller
  - Set operation: stores value with TTL via setex
  - Delete operation: removes key from cache
  - Bypass mode on write: set errors are swallowed silently

# Test Structure

Tests use pytest class-based organization with a shared ``mock_redis`` fixture
(from ``test_foundation/conftest.py``) for Redis client simulation. All tests
are async (pytest-asyncio) to match the real Redis client's async interface.

# Running Tests

Run with: pytest tests/python/test_foundation/test_cache.py
"""

from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.cache.redis import FailureMode, RedisCache


class TestRedisCache:
    """Test suite for RedisCache with failure mode support."""

    @pytest.mark.asyncio
    async def test_get_returns_cached_value(self, mock_redis: AsyncMock) -> None:
        """Test that get returns the cached value on a cache hit.

        **Why this test is important:**
          - Cache hits avoid expensive database or API calls, reducing latency
          - Incorrect deserialization would corrupt cached data
          - The happy path must work correctly before testing failure modes

        **What it tests:**
          - result equals the bytes value stored in Redis
        """
        mock_redis.get.return_value = b'{"name": "test"}'

        cache = RedisCache(client=mock_redis, failure_mode=FailureMode.BYPASS)
        result = await cache.get("key1")
        assert result == b'{"name": "test"}'

    @pytest.mark.asyncio
    async def test_get_returns_none_on_miss(self, mock_redis: AsyncMock) -> None:
        """Test that get returns None on a cache miss.

        **Why this test is important:**
          - Cache misses must return None so callers can fall through to the data source
          - Returning stale or incorrect data on miss would cause data inconsistency
          - Callers depend on None to trigger cache population logic

        **What it tests:**
          - result is None when Redis returns None for the key
        """
        mock_redis.get.return_value = None

        cache = RedisCache(client=mock_redis, failure_mode=FailureMode.BYPASS)
        result = await cache.get("missing")
        assert result is None

    @pytest.mark.asyncio
    async def test_bypass_mode_returns_none_on_connection_error(
        self, mock_redis: AsyncMock
    ) -> None:
        """Test that bypass mode returns None on connection errors.

        **Why this test is important:**
          - Cache failures should not take down the application in bypass mode
          - Graceful degradation means the app works without cache (slower, but functional)
          - This is critical for Redis maintenance windows and transient network issues

        **What it tests:**
          - result is None when Redis raises ConnectionError in bypass mode
        """
        mock_redis.get.side_effect = ConnectionError("Redis down")

        cache = RedisCache(client=mock_redis, failure_mode=FailureMode.BYPASS)
        result = await cache.get("key")
        assert result is None

    @pytest.mark.asyncio
    async def test_error_mode_propagates_connection_error(
        self, mock_redis: AsyncMock
    ) -> None:
        """Test that error mode propagates connection errors to the caller.

        **Why this test is important:**
          - Some use cases require cache availability (e.g., rate limiting, sessions)
          - Error mode lets callers decide how to handle cache failures explicitly
          - Propagation ensures cache outages are visible in monitoring and alerting

        **What it tests:**
          - ConnectionError is raised when Redis is down in error mode
        """
        mock_redis.get.side_effect = ConnectionError("Redis down")

        cache = RedisCache(client=mock_redis, failure_mode=FailureMode.ERROR)
        with pytest.raises(ConnectionError):
            await cache.get("key")

    @pytest.mark.asyncio
    async def test_set_stores_value_with_ttl(self, mock_redis: AsyncMock) -> None:
        """Test that set stores a value with the specified TTL.

        **Why this test is important:**
          - TTL prevents cache entries from becoming stale indefinitely
          - Incorrect TTL values can cause memory exhaustion or serving stale data
          - The setex call must pass key, TTL, and value in the correct order

        **What it tests:**
          - mock_redis.setex is called with ("key", 60, b"value")
        """
        cache = RedisCache(client=mock_redis, failure_mode=FailureMode.BYPASS)

        await cache.set("key", b"value", ttl_seconds=60)
        mock_redis.setex.assert_called_once_with("key", 60, b"value")

    @pytest.mark.asyncio
    async def test_delete_removes_key(self, mock_redis: AsyncMock) -> None:
        """Test that delete removes the specified key from cache.

        **Why this test is important:**
          - Cache invalidation must work correctly to prevent serving stale data after updates
          - Document deletion and permission changes require immediate cache eviction
          - Failure to delete can cause authorization bypass or data leakage

        **What it tests:**
          - mock_redis.delete is called with "key"
        """
        cache = RedisCache(client=mock_redis, failure_mode=FailureMode.BYPASS)

        await cache.delete("key")
        mock_redis.delete.assert_called_once_with("key")

    @pytest.mark.asyncio
    async def test_bypass_mode_swallows_set_errors(self, mock_redis: AsyncMock) -> None:
        """Test that bypass mode swallows errors during set operations.

        **Why this test is important:**
          - Write failures should not interrupt the main application flow in bypass mode
          - The primary data store write has already succeeded; cache is best-effort
          - Users should not see errors caused by cache infrastructure issues

        **What it tests:**
          - No exception is raised when setex fails with ConnectionError in bypass mode
        """
        mock_redis.setex.side_effect = ConnectionError("Redis down")

        cache = RedisCache(client=mock_redis, failure_mode=FailureMode.BYPASS)
        # Should not raise
        await cache.set("key", b"value", ttl_seconds=60)
