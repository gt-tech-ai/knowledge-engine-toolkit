"""Unit tests for the cache builder (factory + config).

Tests that the builder correctly creates cache instances based on CacheKind,
validates config, and provides sensible defaults.

# Running Tests

Run with: pytest tests/python/test_foundation/test_cache_builder.py -v
"""

from __future__ import annotations

from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.cache.builder import (
    CacheConfig,
    CacheKind,
    default_config,
    new_cache_from_config,
)
from techai_webutils.clients.cache.local import LocalCache
from techai_webutils.clients.cache.null import NullCache
from techai_webutils.clients.cache.redis import RedisCache


class TestCacheBuilder:
    """Test suite for cache builder factory."""

    def test_new_cache_local(self) -> None:
        """Test that CacheKind.LOCAL creates a LocalCache.

        **Why this test is important:**
          - The builder must correctly route LOCAL kind to LocalCache
          - Ensures config-driven cache selection works for in-memory caching

        **What it tests:**
          - Returned instance is a LocalCache
        """
        config = CacheConfig(kind=CacheKind.LOCAL)
        cache = new_cache_from_config(config)
        assert isinstance(cache, LocalCache)

    def test_new_cache_null(self) -> None:
        """Test that CacheKind.NULL creates a NullCache.

        **Why this test is important:**
          - The builder must correctly route NULL kind to NullCache
          - Null cache is used when caching is disabled entirely

        **What it tests:**
          - Returned instance is a NullCache
        """
        config = CacheConfig(kind=CacheKind.NULL)
        cache = new_cache_from_config(config)
        assert isinstance(cache, NullCache)

    def test_new_cache_redis(self) -> None:
        """Test that CacheKind.REDIS creates a RedisCache with the provided client.

        **Why this test is important:**
          - Redis is the production cache; builder must wire the client correctly
          - Missing client should not silently create a broken cache

        **What it tests:**
          - Returned instance is a RedisCache
        """
        mock_client = AsyncMock()
        config = CacheConfig(kind=CacheKind.REDIS, redis_client=mock_client)
        cache = new_cache_from_config(config)
        assert isinstance(cache, RedisCache)

    def test_new_cache_redis_requires_client(self) -> None:
        """Test that CacheKind.REDIS raises ValueError when no client is provided.

        **Why this test is important:**
          - A RedisCache without a client would fail at runtime on first operation
          - Fail-fast at construction prevents confusing runtime errors

        **What it tests:**
          - ValueError is raised when redis_client is None with CacheKind.REDIS
        """
        config = CacheConfig(kind=CacheKind.REDIS)
        with pytest.raises(ValueError, match="redis_client"):
            new_cache_from_config(config)

    def test_new_cache_unknown_kind_raises(self) -> None:
        """Test that an unknown kind raises ValueError.

        **Why this test is important:**
          - Config-driven values can contain typos or invalid entries
          - Fail-fast is better than silently using a default

        **What it tests:**
          - ValueError is raised for an invalid kind
        """
        config = CacheConfig(kind="unknown")  # type: ignore[arg-type]
        with pytest.raises(ValueError, match="unknown"):
            new_cache_from_config(config)

    def test_default_config_returns_local(self) -> None:
        """Test that default_config returns LOCAL kind with 300s TTL.

        **Why this test is important:**
          - Sensible defaults enable zero-config local development
          - The default must be LOCAL (no external deps) with a reasonable TTL

        **What it tests:**
          - kind is CacheKind.LOCAL
          - default_ttl is 300
        """
        config = default_config()
        assert config.kind == CacheKind.LOCAL
        assert config.default_ttl == 300
