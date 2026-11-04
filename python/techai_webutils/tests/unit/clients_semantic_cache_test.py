"""Unit tests for the config-selected ``SemanticCache`` factory and the ``stub`` backend."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.semantic_cache import (
    SemanticCacheConfig,
    SemanticCacheKind,
    semantic_cache_from_config,
)
from techai_webutils.clients.semantic_cache.stub import StubSemanticCache
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.cache import Cache
from techai_webutils.core.interfaces.semantic_cache import CacheNamespace

_NS = CacheNamespace("ws-1", "nomic-embed-text", "v1")


@pytest.mark.asyncio
async def test_stub_semantic_cache_always_misses_and_no_ops() -> None:
    """Test that the stub cache never hits and accepts every write.

    **Why this test is important:**
      - The stub is the default; wiring the cache must leave the query path exactly as it is
        today until a real backend is selected.

    **What it tests:**
      - ``lookup`` returns ``None`` even right after a ``store`` of the same query
      - ``invalidate_prefix`` and ``invalidate_all`` return without error
    """
    cache = StubSemanticCache()

    await cache.store("q", b"answer", _NS, ttl_seconds=60)
    hit = await cache.lookup("q", _NS, min_similarity=0.0)
    await cache.invalidate_prefix("ws-1")
    await cache.invalidate_all()

    assert hit is None


def test_semantic_cache_from_config_stub_default_needs_no_collaborators() -> None:
    """Test that the default config builds the stub with no collaborators.

    **Why this test is important:**
      - The graph must boot with no Redis and no embedder (stub-first); a default that needed
        either would break every dev and test composition.

    **What it tests:**
      - the default ``SemanticCacheConfig`` has kind ``stub``, threshold 0.95, TTL 3600
      - the factory with no collaborators returns a ``StubSemanticCache``
    """
    config = SemanticCacheConfig()

    assert (config.kind, config.min_similarity, config.ttl_seconds) == (
        "stub",
        0.95,
        3600,
    )
    assert isinstance(semantic_cache_from_config(config), StubSemanticCache)


def test_semantic_cache_from_config_unknown_kind_raises() -> None:
    """Test that an unknown cache kind fails at build time.

    **Why this test is important:**
      - A typo must stop boot rather than silently run uncached (or worse, half-wired).

    **What it tests:**
      - an unknown kind raises a ``ValueError`` coded ``INVALID_INPUT``
    """
    with pytest.raises(ValueError, match="unknown semantic cache kind") as caught:
        semantic_cache_from_config(SemanticCacheConfig(kind="qdrant"))

    assert isinstance(caught.value, AppError)
    assert caught.value.code is ErrorCode.INVALID_INPUT


def test_semantic_cache_from_config_redis_requires_collaborators() -> None:
    """Test that the redis kind refuses to build without its collaborators.

    **Why this test is important:**
      - The redis backend composes a KV cache, an embedder and a Redis client; building it
        with one missing would only fail on the first query, in production.

    **What it tests:**
      - ``kind=redis`` with only a cache raises ``AppError(INVALID_INPUT)`` naming all three
    """
    config = SemanticCacheConfig(kind=SemanticCacheKind.REDIS)

    with pytest.raises(AppError, match="requires cache, embedding and redis") as caught:
        semantic_cache_from_config(config, cache=MagicMock(spec=Cache))

    assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_stub_semantic_cache_primitives_miss() -> None:
    """Test that the stub's ``exact`` and ``nearest`` both miss.

    **Why this test is important:**
      - The stub is the zero-infra default; a tier chain over it must behave as uncached.

    **What it tests:**
      - ``exact`` and ``nearest`` return ``None``
    """
    cache = StubSemanticCache()
    ns = CacheNamespace("ws-1", "m", "v1")

    assert await cache.exact("q", ns) is None
    assert await cache.nearest("q", ns) is None
