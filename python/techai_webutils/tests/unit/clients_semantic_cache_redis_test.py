"""Unit tests for ``RedisSemanticCache`` over a mocked Redis client and KV cache.

The KNN ranking itself needs a real search server and is proven in the integration suite;
these tests pin the backend's decisions: the exact-repeat fast path, the similarity
threshold, the write layout, namespace scoping, both ``FT.SEARCH`` reply shapes and
fail-open.
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

_EXPECTED_AWAIT_COUNT = 2
_EXPECTED_MAPPING_COUNT = 32
_EXPECTED_CALL_COUNT = 2

_NS = CacheNamespace("ws-1", "stub-embed", "8")
_OTHER = CacheNamespace("ws-2", "stub-embed", "8")


class _Harness:
    """The mocked collaborators of one cache under test."""

    def __init__(
        self,
        *,
        docs: list[SimpleNamespace] | None = None,
        strip_trailing_punctuation: bool = False,
    ) -> None:
        """Mock Redis (raw ``FT.SEARCH`` plus an ``ft()`` handle).

        The KV cache and metrics.

        ``FT.SEARCH`` answers in the RESP2 array shape Valkey Search returns.
        """
        self.redis = MagicMock(spec=Redis)
        self.redis.execute_command = AsyncMock(return_value=_array_reply(docs or []))
        self.search = MagicMock()
        self.search.create_index = AsyncMock()
        self.search.dropindex = AsyncMock()
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
            SemanticCacheConfig(
                kind=SemanticCacheKind.REDIS,
                index_prefix="sc",
                strip_trailing_punctuation=strip_trailing_punctuation,
            ),
            cache=self.kv,
            embedding=self.embedding,
            redis=self.redis,
            metrics=metrics,
        )


async def _aiter(items: list[str]) -> AsyncGenerator[str]:
    """Yield ``items`` as an async iterator (the shape of ``scan_iter``).

    Yields:
        ``items`` as an async iterator (the shape of ``scan_iter``).

    """
    for item in items:
        await asyncio.sleep(0)
        yield item


def _array_reply(docs: list[SimpleNamespace]) -> list[object]:
    """Return ``docs`` as the RESP2 ``FT.SEARCH`` array.

    ``[total, id, [field, value, …], …]``.

    Returns:
        ``docs`` as the RESP2 ``FT.SEARCH`` array.

    """
    reply: list[object] = [len(docs)]
    for i, doc in enumerate(docs):
        reply += [
            f"sc:d:{i}".encode(),
            [
                b"key",
                doc.key.encode(),
                b"query",
                doc.query.encode(),
                b"dist",
                doc.dist.encode(),
            ],
        ]
    return reply


def _map_reply(docs: list[SimpleNamespace]) -> dict[bytes, object]:
    """Return ``docs`` as the RESP3 ``FT.SEARCH`` map Redis Stack returns.

    Returns:
        ``docs`` as the RESP3 ``FT.SEARCH`` map Redis Stack returns.

    """
    return {
        b"total_results": len(docs),
        b"results": [
            {
                b"id": f"sc:d:{i}".encode(),
                b"extra_attributes": {
                    b"key": doc.key.encode(),
                    b"query": doc.query.encode(),
                    b"dist": doc.dist.encode(),
                },
            }
            for i, doc in enumerate(docs)
        ],
    }


def _doc(distance: float) -> SimpleNamespace:
    """Return a FT.SEARCH result document at cosine ``distance``.

    Returns:
        A FT.SEARCH result document at cosine ``distance``.

    """
    return SimpleNamespace(
        key="sc:p:ws-1:k:abc", query="the cached question", dist=str(distance)
    )


@pytest.mark.asyncio
async def test_redis_semantic_cache_exact_hit_skips_embedding() -> None:
    """Test that an exact (normalized) repeat is served without embedding.

    **Why this test is important:**
      - The fast path is what makes a repeated question cheap; embedding it anyway would
        add a model call to every hit.

    **What it tests:**
      - ``"  What is X? "`` hits a payload stored for ``"what is x?"``, ``similarity ==
        1.0``
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
    harness.redis.execute_command.assert_not_awaited()
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
async def test_redis_semantic_cache_reads_the_resp3_map_reply() -> None:
    """Test that a KNN hit is read from the RESP3 map reply as well as the RESP2 array.

    **Why this test is important:**
      - Redis Stack answers ``FT.SEARCH`` as a RESP3 map and Valkey Search as the RESP2
        array on the same connection; reading only one shape turns every lookup on the
        other engine into a silent miss.

    **What it tests:**
      - a map reply with a neighbour at distance 0.03 returns its payload at similarity
        0.97
      - an empty map reply is a miss
    """
    harness = _Harness()
    harness.redis.execute_command = AsyncMock(return_value=_map_reply([_doc(0.03)]))
    harness.kv.get = AsyncMock(side_effect=[None, b"cached answer"])
    empty = _Harness()
    empty.redis.execute_command = AsyncMock(return_value=_map_reply([]))

    hit = await harness.cache.lookup("what's x", _NS, min_similarity=0.95)

    assert hit is not None
    assert hit.payload == b"cached answer"
    assert hit.similarity == pytest.approx(0.97)
    assert await empty.cache.lookup("q", _NS, min_similarity=0.95) is None


@pytest.mark.asyncio
async def test_redis_semantic_cache_knn_miss_below_threshold() -> None:
    """Test that a neighbour below the threshold, or with an expired payload, is a miss.

    **Why this test is important:**
      - Serving a merely related answer is worse than no cache; and an index document
        whose payload expired must not resurrect a stale hit.

    **What it tests:**
      - distance 0.2 (similarity 0.8 < 0.95) → ``None`` (``nearest`` reads the neighbour's
        payload whatever its similarity, since a verifier band may serve it; ``lookup``
        applies the threshold)
      - distance 0.01 but the payload is gone → ``None``
    """
    far = _Harness(docs=[_doc(0.2)])
    stale = _Harness(docs=[_doc(0.01)])

    assert await far.cache.lookup("q", _NS, min_similarity=0.95) is None
    assert far.kv.get.await_count == _EXPECTED_AWAIT_COUNT
    assert await stale.cache.lookup("q", _NS, min_similarity=0.95) is None


@pytest.mark.asyncio
async def test_redis_semantic_cache_store_writes_payload_with_ttl() -> None:
    """Test that a store writes the payload with its TTL and indexes the query vector.

    **Why this test is important:**
      - The payload TTL bounds staleness; the index document is what a later rephrasing
        finds.

    **What it tests:**
      - the index is created once across two stores, over the doc prefix, with a COSINE
        vector of the embedder's dimension
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
    assert len(mapping["embedding"]) == _EXPECTED_MAPPING_COUNT
    harness.redis.expire.assert_any_await(doc_key, 120)


@pytest.mark.asyncio
async def test_redis_semantic_cache_lookup_fails_open() -> None:
    """Test that a backend failure during lookup is a counted miss.

    **Why this test is important:**
      - A Redis outage must look exactly like today's uncached path, never an error to the
        user.

    **What it tests:**
      - a raising KV get, a raising search and a raising embedder each return ``None``
      - each failure increments ``semantic_cache_errors_total{op="lookup"}``
    """
    kv_down = _Harness()
    kv_down.kv.get = AsyncMock(side_effect=RedisConnectionError("down"))
    search_down = _Harness()
    search_down.redis.execute_command = AsyncMock(
        side_effect=RedisConnectionError("down")
    )
    embed_down = _Harness()
    embed_down.embedding.embed = AsyncMock(side_effect=RuntimeError("ollama down"))  # type: ignore[method-assign]

    for harness in (kv_down, search_down, embed_down):
        assert await harness.cache.lookup("q", _NS, min_similarity=0.9) is None
        harness.errors.inc.assert_called_once_with(op="lookup")


@pytest.mark.asyncio
async def test_redis_semantic_cache_store_fails_open() -> None:
    """Test that a backend failure during store is a counted no-op.

    **Why this test is important:**
      - Writing through to the cache happens after the answer exists; failing it must
        never fail the answer.

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
    assert harness.errors.inc.call_count == _EXPECTED_CALL_COUNT
    assert harness.search.create_index.await_count == _EXPECTED_AWAIT_COUNT


@pytest.mark.asyncio
async def test_redis_semantic_cache_namespace_isolation() -> None:
    """Test that the KNN query is scoped to the asking namespace's tag.

    **Why this test is important:**
      - The tag filter is the only thing stopping one workspace's (or clearance's) cached
        answer from matching another's query.

    **What it tests:**
      - the FT query string filters on ``@ns:{<tag>}`` of the namespace asked, and the two
        namespaces' queries differ
    """
    harness = _Harness()

    await harness.cache.lookup("q", _NS, min_similarity=0.9)
    await harness.cache.lookup("q", _OTHER, min_similarity=0.9)

    first, second = (
        call.args[2] for call in harness.redis.execute_command.await_args_list
    )
    assert f"@ns:{{{_NS.tag()}}}" in first
    assert f"@ns:{{{_OTHER.tag()}}}" in second


@pytest.mark.asyncio
async def test_redis_semantic_cache_invalidation() -> None:
    """Test that prefix invalidation is scoped to the workspace.

    And ``invalidate_all`` clears all.

    **Why this test is important:**
      - Evicting one workspace after a document change must not flush every tenant's
        cache.

    **What it tests:**
      - ``invalidate_prefix("ws-1")`` scans ``sc:p:ws-1:*`` and ``sc:d:ws-1:*`` (never
        ``ws-10``) and unlinks hits
      - glob characters in the prefix are escaped
      - ``invalidate_all`` drops the index (no ``DD``: Valkey Search rejects it), then
        unlinks every index document and payload key itself
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
    assert matches == [
        "sc:p:ws-1:*",
        "sc:d:ws-1:*",
        "sc:p:w\\*:*",
        "sc:d:w\\*:*",
        "sc:d:*",
        "sc:p:*",
    ]
    harness.redis.unlink.assert_any_await("sc:p:ws-1:x")
    harness.search.dropindex.assert_awaited_once_with(delete_documents=False)
    assert isinstance(harness.cache, RedisSemanticCache)

    down = _Harness()
    down.redis.scan_iter = MagicMock(side_effect=RedisConnectionError("down"))
    down.search.dropindex = AsyncMock(side_effect=RedisConnectionError("down"))
    await down.cache.invalidate_prefix("ws-1")
    await down.cache.invalidate_all()
    assert down.errors.inc.call_count == _EXPECTED_CALL_COUNT
    down.errors.inc.assert_called_with(op="invalidate")


@pytest.mark.asyncio
async def test_redis_semantic_cache_primitives_split_exact_and_nearest() -> None:
    """Test that ``exact`` never embeds.

    And ``nearest`` returns any neighbour unthresholded.

    **Why this test is important:**
      - The match tiers apply their own thresholds (a per-workspace similarity, a verifier
        band); a backend that thresholded inside ``nearest`` would hide the band's
        candidates.

    **What it tests:**
      - ``exact`` misses without embedding or searching when no payload is stored
      - ``nearest`` returns a neighbour at similarity 0.80 with its payload and cached
        query
    """
    harness = _Harness(docs=[_doc(0.2)])

    assert await harness.cache.exact("what's x", _NS) is None
    harness.embedding.embed.assert_not_awaited()
    harness.redis.execute_command.assert_not_awaited()

    harness.kv.get = AsyncMock(return_value=b"near answer")
    near = await harness.cache.nearest("what's x", _NS)

    assert near is not None
    assert near.payload == b"near answer"
    assert near.similarity == pytest.approx(0.8)
    assert near.source_query == "the cached question"


@pytest.mark.asyncio
async def test_redis_semantic_cache_strips_trailing_punctuation_when_configured() -> None:
    """Test that the optional normalization makes "What is X?" and "what is x" one key.

    **Why this test is important:**
      - The exact tier hashes the normalized question; with the flag on, a repeat that
        differs only by a trailing question mark is served without an embedding.

    **What it tests:**
      - with the flag on, both phrasings read the same payload key
      - with it off (the default), they read different keys
    """
    on = _Harness(strip_trailing_punctuation=True)
    await on.cache.exact("What is X?", _NS)
    await on.cache.exact("what is x", _NS)
    keys_on = [c.args[0] for c in on.kv.get.await_args_list]

    off = _Harness()
    await off.cache.exact("What is X?", _NS)
    await off.cache.exact("what is x", _NS)
    keys_off = [c.args[0] for c in off.kv.get.await_args_list]

    assert keys_on[0] == keys_on[1]
    assert keys_off[0] != keys_off[1]


@pytest.mark.asyncio
async def test_redis_semantic_cache_primitives_fail_open() -> None:
    """Test that a backend error in either primitive is a miss and is counted.

    **Why this test is important:**
      - A Redis outage must look exactly like an uncached query, whichever tier asked.

    **What it tests:**
      - a raising KV read makes ``exact`` and a raising search makes ``nearest`` return
        ``None``
      - ``semantic_cache_errors_total{op="lookup"}`` increments for each
    """
    harness = _Harness()
    harness.kv.get = AsyncMock(side_effect=RedisConnectionError("down"))
    harness.redis.execute_command = AsyncMock(side_effect=RedisConnectionError("down"))

    assert await harness.cache.exact("q", _NS) is None
    assert await harness.cache.nearest("q", _NS) is None
    assert harness.errors.inc.call_count == _EXPECTED_CALL_COUNT
