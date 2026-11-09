"""Integration tests for ``RedisSemanticCache`` against real Redis Stack.

And Valkey Search servers.

The unit suite mocks the client; only a real server proves the index schema, the
namespace-tag filter, KNN ranking by cosine distance, TTL expiry and SCAN-based eviction.
Every test runs on both
engines: Redis Stack (RediSearch) and Valkey Bundle (the ``search`` module, as ElastiCache
for Valkey 9 serves it).
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock

import pytest
import pytest_asyncio
from redis.asyncio import Redis
from testcontainers.redis import RedisContainer

from techai_webutils.clients.cache.redis import RedisCache
from techai_webutils.clients.cache.types import FailureMode
from techai_webutils.clients.semantic_cache import (
    SemanticCacheConfig,
    SemanticCacheKind,
    semantic_cache_from_config,
)
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.semantic_cache import CacheNamespace

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Iterator

    from techai_webutils.core.interfaces.semantic_cache import SemanticCache

_IMAGES = ["redis/redis-stack-server:7.4.0-v1", "valkey/valkey-bundle:9.1.4"]
"""The two search-capable engines: Redis Stack ships RediSearch (plain redis:7 does not),
and Valkey Bundle 9.1 ships the ``search`` module, the Valkey major ElastiCache for Valkey
serves."""

_NS = CacheNamespace("ws-1", "axes", "3")
_OTHER = CacheNamespace("ws-2", "axes", "3")

_VECTORS = {
    "cats": [1.0, 0.0, 0.0],
    "kittens": [0.99, 0.14, 0.0],
    "dogs": [0.6, 0.8, 0.0],
    "taxes": [0.0, 0.0, 1.0],
}
"""Hand-placed unit vectors: kittens ≈ cats (cos 0.99), dogs further (cos 0.6), taxes
orthogonal."""


def _axis_embedding() -> AsyncMock:
    """Return a mock of the embedding boundary over ``_VECTORS``.

    So the neighbours are known.

    Returns:
        An ``EmbeddingProvider`` mock (model ``axes``, dimension 3).

    """

    async def embed(text: str, **_: object) -> EmbeddingResult:  # noqa: RUF029 - the port is async
        """Return the hand-placed vector for ``text``.

        Returns:
            The hand-placed vector for ``text``.

        """
        return EmbeddingResult(embedding=_VECTORS[text], model="axes", token_count=0)

    embedding = AsyncMock(spec=EmbeddingProvider)
    embedding.embed.side_effect = embed
    embedding.dimension.return_value = 3
    embedding.model_name.return_value = "axes"
    return embedding


@pytest.fixture(scope="module", params=_IMAGES)
def stack_container(request: pytest.FixtureRequest) -> Iterator[object]:
    """Start one search-capable server per image for the module.

    Yields:
        One search-capable server per image for the module.

    """

    with RedisContainer(request.param) as container:
        yield container


@pytest_asyncio.fixture
async def semantic_cache(stack_container: object) -> AsyncIterator[SemanticCache]:
    """Yield a fresh redis-kind cache on the container (flushed before each test).

    Yields:
        A fresh redis-kind cache on the container (flushed before each test).

    """
    host = stack_container.get_container_host_ip()  # type: ignore[attr-defined]
    port = int(stack_container.get_exposed_port(stack_container.port))  # type: ignore[attr-defined]
    client = Redis(host=host, port=port)
    await client.flushall()
    try:
        yield semantic_cache_from_config(
            SemanticCacheConfig(kind=SemanticCacheKind.REDIS, index_prefix="it"),
            cache=RedisCache(client, FailureMode.ERROR),
            embedding=_axis_embedding(),
            redis=client,
        )
    finally:
        await client.aclose()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_round_trip_and_knn_ranking(semantic_cache: SemanticCache) -> None:
    """Test that a stored answer is found by its rephrasing, ranked by cosine distance.

    **Why this test is important:**
      - Only a real RediSearch proves the index schema, the tag filter and the KNN syntax.

    **What it tests:**
      - after storing ``cats`` and ``dogs``, ``kittens`` hits the ``cats`` payload (≈0.99)
      - ``taxes`` (orthogonal) misses at 0.9
      - another workspace never sees the entries
    """
    await semantic_cache.store("cats", b"cat answer", _NS, ttl_seconds=60)
    await semantic_cache.store("dogs", b"dog answer", _NS, ttl_seconds=60)

    hit = await semantic_cache.lookup("kittens", _NS, min_similarity=0.9)
    miss = await semantic_cache.lookup("taxes", _NS, min_similarity=0.9)
    other = await semantic_cache.lookup("kittens", _OTHER, min_similarity=0.0)

    assert hit is not None
    assert (hit.payload, hit.source_query) == (b"cat answer", "cats")
    assert hit.similarity == pytest.approx(0.99, abs=0.01)
    assert miss is None
    assert other is None


@pytest.mark.integration
@pytest.mark.asyncio
async def test_ttl_expiry(semantic_cache: SemanticCache) -> None:
    """Test that an entry expires with its TTL.

    **Why this test is important:**
      - The TTL bounds how stale a served answer can be.

    **What it tests:**
      - an entry stored with ``ttl_seconds=1`` misses after 1.5 s
    """
    await semantic_cache.store("cats", b"cat answer", _NS, ttl_seconds=1)
    assert await semantic_cache.lookup("cats", _NS, min_similarity=0.9) is not None

    await asyncio.sleep(1.5)

    assert await semantic_cache.lookup("kittens", _NS, min_similarity=0.0) is None


@pytest.mark.integration
@pytest.mark.asyncio
async def test_invalidate_prefix_drops_only_the_workspace(
    semantic_cache: SemanticCache,
) -> None:
    """Test that evicting one workspace leaves another's entries.

    **Why this test is important:**
      - A document change in one tenant must not flush every tenant's cache.

    **What it tests:**
      - after ``invalidate_prefix("ws-1")``, ws-1 misses and ws-2 still hits
      - ``invalidate_all`` then clears ws-2 too, and a later store works again
    """
    await semantic_cache.store("cats", b"one", _NS, ttl_seconds=60)
    await semantic_cache.store("cats", b"two", _OTHER, ttl_seconds=60)

    await semantic_cache.invalidate_prefix("ws-1")

    assert await semantic_cache.lookup("kittens", _NS, min_similarity=0.9) is None
    kept = await semantic_cache.lookup("kittens", _OTHER, min_similarity=0.9)
    assert kept is not None
    assert kept.payload == b"two"

    await semantic_cache.invalidate_all()
    assert await semantic_cache.lookup("cats", _OTHER, min_similarity=0.9) is None
    await semantic_cache.store("dogs", b"three", _OTHER, ttl_seconds=60)
    again = await semantic_cache.lookup("dogs", _OTHER, min_similarity=0.9)
    assert again is not None
