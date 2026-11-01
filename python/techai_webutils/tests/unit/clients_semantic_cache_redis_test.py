"""Unit tests for ``RedisSemanticCache`` over a mocked Redis client and KV cache.

The KNN ranking itself needs a real RediSearch server and is proven in the integration suite;
these tests pin the backend's decisions: the exact-repeat fast path, the similarity threshold,
the write layout, namespace scoping and fail-open.
"""

from __future__ import annotations

import asyncio
from types import SimpleNamespace
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest
from redis.asyncio import Redis
from redis.exceptions import ConnectionError as RedisConnectionError

from techai_webutils.clients.embedding.stub import StubEmbeddingProvider
from techai_webutils.clients.semantic_cache import (
    SemanticCacheConfig,
    SemanticCacheKind,
    semantic_cache_from_config,
)
from techai_webutils.clients.semantic_cache.redis import RedisSemanticCache
from techai_webutils.core.interfaces.cache import Cache
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
from techai_webutils.core.interfaces.semantic_cache import CachedAnswer, CacheNamespace

if TYPE_CHECKING:
    from collections.abc import AsyncGenerator

_NS = CacheNamespace("ws-1", "stub-embed", "8")
_OTHER = CacheNamespace("ws-2", "stub-embed", "8")


class _Harness:
    """The mocked collaborators of one cache under test."""

    def __init__(self, *, docs: list[SimpleNamespace] | None = None) -> None:
        """Mock Redis (with an ``ft()`` search handle), the KV cache and metrics."""
        self.search = MagicMock()
        self.search.search = AsyncMock(return_value=SimpleNamespace(docs=docs or []))
        self.search.create_index = AsyncMock()
        self.search.dropindex = AsyncMock()
        self.redis = MagicMock(spec=Redis)
        self.redis.ft = MagicMock(return_value=self.search)
        self.redis.hset = AsyncMock()
        self.redis.expire = AsyncMock()
        self.redis.unlink = AsyncMock()
        self.redis.scan_iter = MagicMock(side_effect=lambda **_kw: _aiter([]))
        self.kv = MagicMock(spec=Cache)
        self.kv.get = AsyncMock(return_value=None)
        self.kv.set = AsyncMock()
        self.embedding = StubEmbeddingProvider("stub-embed", 8)
        self.embedding.embed = AsyncMock(wraps=self.embedding.embed)  # type: ignore[method-assign]
        self.errors = MagicMock(spec=MetricCounter)
        metrics = MagicMock(spec=MetricsProvider)
        metrics.counter = MagicMock(return_value=self.errors)
        self.cache = semantic_cache_from_config(
            SemanticCacheConfig(kind=SemanticCacheKind.REDIS, index_prefix="sc"),
            cache=self.kv,
            embedding=self.embedding,
            redis=self.redis,
            metrics=metrics,
        )


async def _aiter(items: list[str]) -> AsyncGenerator[str]:
    """Yield ``items`` as an async iterator (the shape of ``scan_iter``)."""
    for item in items:
        await asyncio.sleep(0)
        yield item


def _doc(distance: float) -> SimpleNamespace:
    """Return a FT.SEARCH result document at cosine ``distance``."""
    return SimpleNamespace(
        key="sc:p:ws-1:k:abc", query="the cached question", dist=str(distance)
    )


@pytest.mark.asyncio
async def test_redis_semantic_cache_exact_hit_skips_embedding() -> None:
    """Test that an exact (normalized) repeat is served without embedding.

    **Why this test is important:**
      - The fast path is what makes a repeated question cheap; embedding it anyway would add a
        model call to every hit.

    **What it tests:**
      - ``"  What is X? "`` hits a payload stored for ``"what is x?"``, ``similarity == 1.0``
      - the embedder is never called and no KNN search runs
    """
    harness = _Harness()
    harness.kv.get = AsyncMock(return_value=b"answer")

    hit = await harness.cache.lookup("  What  is X? ", _NS, min_similarity=0.95)

    assert hit == CachedAnswer(
        payload=b"answer",
        similarity=1.0,
        source_query="  What  is X? ",
        embedding_model="stub-embed",
    )
    harness.embedding.embed.assert_not_awaited()
    harness.search.search.assert_not_awaited()
    assert harness.kv.get.await_args.args[0].startswith(f"sc:p:ws-1:{_NS.tag()}:")


@pytest.mark.asyncio
async def test_redis_semantic_cache_knn_hit_above_threshold() -> None:
    """Test that a near-duplicate at or above the threshold is a hit.

    **Why this test is important:**
      - The semantic hit is the epic's value: a rephrased question served from cache.

    **What it tests:**
      - a KNN neighbour at distance 0.03 (similarity 0.97 ≥ 0.95) returns its payload with
        ``similarity`` 0.97 and the cached query as ``source_query``
    """
    harness = _Harness(docs=[_doc(0.03)])
    harness.kv.get = AsyncMock(side_effect=[None, b"cached answer"])

    hit = await harness.cache.lookup("what's x", _NS, min_similarity=0.95)

    assert hit is not None
    assert hit.payload == b"cached answer"
    assert hit.similarity == pytest.approx(0.97)
    assert hit.source_query == "the cached question"
    assert harness.kv.get.await_args_list[1].args == ("sc:p:ws-1:k:abc",)


@pytest.mark.asyncio
async def test_redis_semantic_cache_knn_miss_below_threshold() -> None:
    """Test that a neighbour below the threshold, or with an expired payload, is a miss.

    **Why this test is important:**
      - Serving a merely related answer is worse than no cache; and an index document whose
        payload expired must not resurrect a stale hit.

    **What it tests:**
      - distance 0.2 (similarity 0.8 < 0.95) → ``None`` without reading the payload
      - distance 0.01 but the payload is gone → ``None``
    """
    far = _Harness(docs=[_doc(0.2)])
    stale = _Harness(docs=[_doc(0.01)])

    assert await far.cache.lookup("q", _NS, min_similarity=0.95) is None
    assert far.kv.get.await_count == 1
    assert await stale.cache.lookup("q", _NS, min_similarity=0.95) is None


@pytest.mark.asyncio
async def test_redis_semantic_cache_store_writes_payload_with_ttl() -> None:
    """Test that a store writes the payload with its TTL and indexes the query vector.

    **Why this test is important:**
      - The payload TTL bounds staleness; the index document is what a later rephrasing finds.

    **What it tests:**
      - the index is created once across two stores, over the doc prefix, with a COSINE vector
        of the embedder's dimension
      - ``Cache.set`` receives the payload key, the payload and ``ttl_seconds``
      - the index hash carries the namespace tag, the payload key, the query and a float32
        vector (8 dims → 32 bytes), and expires with the payload
    """
    harness = _Harness()

    await harness.cache.store("What is X?", b"answer", _NS, ttl_seconds=120)
    await harness.cache.store("Other", b"two", _NS, ttl_seconds=120)

    harness.search.create_index.assert_awaited_once()
    payload_key, payload = harness.kv.set.await_args_list[0].args[:2]
    assert payload == b"answer"
    assert harness.kv.set.await_args_list[0].kwargs == {"ttl_seconds": 120}
    assert payload_key.startswith(f"sc:p:ws-1:{_NS.tag()}:")
    doc_key = harness.redis.hset.await_args_list[0].args[0]
    mapping = harness.redis.hset.await_args_list[0].kwargs["mapping"]
    assert doc_key == payload_key.replace("sc:p:", "sc:d:", 1)
    assert (mapping["ns"], mapping["key"], mapping["query"]) == (
        _NS.tag(),
        payload_key,
        "What is X?",
    )
    assert len(mapping["embedding"]) == 32
    harness.redis.expire.assert_any_await(doc_key, 120)


@pytest.mark.asyncio
async def test_redis_semantic_cache_lookup_fails_open() -> None:
    """Test that a backend failure during lookup is a counted miss.

    **Why this test is important:**
      - A Redis outage must look exactly like today's uncached path, never an error to the user.

    **What it tests:**
      - a raising KV get, a raising search and a raising embedder each return ``None``
      - each failure increments ``semantic_cache_errors_total{op="lookup"}``
    """
    kv_down = _Harness()
    kv_down.kv.get = AsyncMock(side_effect=RedisConnectionError("down"))
    search_down = _Harness()
    search_down.search.search = AsyncMock(side_effect=RedisConnectionError("down"))
    embed_down = _Harness()
    embed_down.embedding.embed = AsyncMock(side_effect=RuntimeError("ollama down"))  # type: ignore[method-assign]

    for harness in (kv_down, search_down, embed_down):
        assert await harness.cache.lookup("q", _NS, min_similarity=0.9) is None
        harness.errors.inc.assert_called_once_with(op="lookup")


@pytest.mark.asyncio
async def test_redis_semantic_cache_store_fails_open() -> None:
    """Test that a backend failure during store is a counted no-op.

    **Why this test is important:**
      - Writing through to the cache happens after the answer exists; failing it must never
        fail the answer.

    **What it tests:**
      - a raising ``hset`` returns normally and increments ``…{op="store"}``
      - a failing index creation is retried on the next store
      - an index that already exists counts as created
    """
    harness = _Harness()
    harness.redis.hset = AsyncMock(side_effect=RedisConnectionError("down"))
    harness.search.create_index = AsyncMock(
        side_effect=[RedisConnectionError("down"), None]
    )

    await harness.cache.store("q", b"a", _NS, ttl_seconds=60)
    await harness.cache.store("q", b"a", _NS, ttl_seconds=60)

    harness.errors.inc.assert_called_with(op="store")
    assert harness.errors.inc.call_count == 2
    assert harness.search.create_index.await_count == 2


@pytest.mark.asyncio
async def test_redis_semantic_cache_namespace_isolation() -> None:
    """Test that the KNN query is scoped to the asking namespace's tag.

    **Why this test is important:**
      - The tag filter is the only thing stopping one workspace's (or clearance's) cached answer
        from matching another's query.

    **What it tests:**
      - the FT query string filters on ``@ns:{<tag>}`` of the namespace asked, and the two
        namespaces' queries differ
    """
    harness = _Harness()

    await harness.cache.lookup("q", _NS, min_similarity=0.9)
    await harness.cache.lookup("q", _OTHER, min_similarity=0.9)

    first, second = (
        call.args[0].query_string() for call in harness.search.search.await_args_list
    )
    assert f"@ns:{{{_NS.tag()}}}" in first
    assert f"@ns:{{{_OTHER.tag()}}}" in second


@pytest.mark.asyncio
async def test_redis_semantic_cache_invalidation() -> None:
    """Test that prefix invalidation is scoped to the workspace and ``invalidate_all`` clears all.

    **Why this test is important:**
      - Evicting one workspace after a document change must not flush every tenant's cache.

    **What it tests:**
      - ``invalidate_prefix("ws-1")`` scans ``sc:p:ws-1*`` and ``sc:d:ws-1*`` and unlinks hits
      - glob characters in the prefix are escaped
      - ``invalidate_all`` drops the index with its documents and unlinks every payload key
      - a failing scan or drop is counted as ``op="invalidate"`` and never raised
    """
    harness = _Harness()
    harness.redis.scan_iter = MagicMock(
        side_effect=lambda **kw: _aiter([f"{kw['match'][:-1]}x"])
    )

    await harness.cache.invalidate_prefix("ws-1")
    await harness.cache.invalidate_prefix("w*")
    await harness.cache.invalidate_all()

    matches = [call.kwargs["match"] for call in harness.redis.scan_iter.call_args_list]
    assert matches == ["sc:p:ws-1*", "sc:d:ws-1*", "sc:p:w\\**", "sc:d:w\\**", "sc:p:*"]
    harness.redis.unlink.assert_any_await("sc:p:ws-1x")
    harness.search.dropindex.assert_awaited_once_with(delete_documents=True)
    assert isinstance(harness.cache, RedisSemanticCache)

    down = _Harness()
    down.redis.scan_iter = MagicMock(side_effect=RedisConnectionError("down"))
    down.search.dropindex = AsyncMock(side_effect=RedisConnectionError("down"))
    await down.cache.invalidate_prefix("ws-1")
    await down.cache.invalidate_all()
    assert down.errors.inc.call_count == 2
    down.errors.inc.assert_called_with(op="invalidate")
