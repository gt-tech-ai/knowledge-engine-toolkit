"""Integration tests for RedisCache against a real Redis instance.

The Python parity to ``go/tests/integration/clients_redis_cache_test.go``.
Unit tests cover the BYPASS/ERROR error branches with mocks; this suite confirms
the get/set/delete/exists/TTL behavior against a real connection — TTL expiry in
particular cannot be validated with a mock or a fake clock.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

import pytest

if TYPE_CHECKING:
    from techai_webutils.clients.cache.redis import RedisCache


def _unique_key(request: pytest.FixtureRequest, label: str) -> str:
    """Test-scoped key so tests sharing one Redis instance never collide."""
    return f"{request.node.name}:{label}"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_set_then_get_returns_value(
    redis_cache: RedisCache, request: pytest.FixtureRequest
) -> None:
    """Test that a value written with set is returned by get.

    **Why this test is important:**
      - This is the fundamental cache-aside happy path callers rely on; a broken
        hit means every cached read silently falls through to the source of truth
      - Exercises the real round-trip through redis-py that mocks cannot prove

    **What it tests:**
      - get returns the exact bytes previously written with set
    """
    key = _unique_key(request, "hit")
    await redis_cache.set(key, b"hello-redis")

    assert await redis_cache.get(key) == b"hello-redis"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_get_unknown_key_returns_none(
    redis_cache: RedisCache, request: pytest.FixtureRequest
) -> None:
    """Test that get on a never-written key returns None (cache miss).

    **Why this test is important:**
      - Callers use None to decide whether to invoke the loader; a wrong return
        here breaks the cache-aside pattern and can surface stale or empty data

    **What it tests:**
      - get on an unknown key returns None
    """
    assert await redis_cache.get(_unique_key(request, "miss")) is None


@pytest.mark.integration
@pytest.mark.asyncio
async def test_delete_removes_key(
    redis_cache: RedisCache, request: pytest.FixtureRequest
) -> None:
    """Test that delete removes a key so a subsequent get misses.

    **Why this test is important:**
      - Delete is the invalidation path used by writes and mutation hooks; a
        failure means stale entries persist indefinitely

    **What it tests:**
      - The key is present before delete and absent (None) after delete
    """
    key = _unique_key(request, "del")
    await redis_cache.set(key, b"to-be-deleted")
    assert await redis_cache.get(key) == b"to-be-deleted"

    await redis_cache.delete(key)

    assert await redis_cache.get(key) is None


@pytest.mark.integration
@pytest.mark.asyncio
async def test_exists_reflects_presence(
    redis_cache: RedisCache, request: pytest.FixtureRequest
) -> None:
    """Test that exists reports True only while the key is present.

    **Why this test is important:**
      - exists gates conditional writes/reads; a wrong answer causes double work
        or skipped population

    **What it tests:**
      - exists is False for an unknown key, True after set, False after delete
    """
    key = _unique_key(request, "exists")
    assert await redis_cache.exists(key) is False

    await redis_cache.set(key, b"present")
    assert await redis_cache.exists(key) is True

    await redis_cache.delete(key)
    assert await redis_cache.exists(key) is False


@pytest.mark.integration
@pytest.mark.asyncio
async def test_ttl_expiry_removes_key(
    redis_cache: RedisCache, request: pytest.FixtureRequest
) -> None:
    """Test that a key written with a short TTL is gone after the TTL elapses.

    **Why this test is important:**
      - TTL enforcement by Redis is the only mechanism preventing stale entries;
        it cannot be validated with mocks or a fake clock — only a real server

    **What it tests:**
      - The key is present immediately after set with a 1s TTL
      - The key is absent (None) after the TTL elapses
    """
    key = _unique_key(request, "ttl")
    await redis_cache.set(key, b"expires-soon", ttl_seconds=1)
    assert await redis_cache.get(key) == b"expires-soon"

    await asyncio.sleep(1.5)

    assert await redis_cache.get(key) is None
