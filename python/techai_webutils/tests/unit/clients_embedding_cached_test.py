"""Unit tests for ``CachedEmbedding``, which reuses a text's vector through a ``Cache``."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.embedding.cached import CachedEmbedding
from techai_webutils.core.interfaces.cache import Cache
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider


def _inner(model: str = "m") -> MagicMock:
    """Return a mocked provider whose vector for a text is ``[len(text), 0.5]``."""
    inner = MagicMock(spec=EmbeddingProvider)

    def batch(
        texts: list[str], *, input_type: str = "document"
    ) -> list[EmbeddingResult]:
        del input_type
        return [EmbeddingResult([float(len(t)), 0.5], model, 7) for t in texts]

    inner.embed_batch = AsyncMock(side_effect=batch)
    inner.model_name.return_value = model
    inner.dimension.return_value = 2
    return inner


def _cache() -> MagicMock:
    """Return a mocked cache backed by a dict, so a stored value reads back."""
    store: dict[str, bytes] = {}
    cache = MagicMock(spec=Cache)

    def get(key: str) -> bytes | None:
        return store.get(key)

    def put(key: str, value: bytes, ttl_seconds: int = 300) -> None:
        del ttl_seconds
        store[key] = value

    cache.get = AsyncMock(side_effect=get)
    cache.set = AsyncMock(side_effect=put)
    return cache


@pytest.mark.asyncio
async def test_cached_vectors_are_reused_and_misses_embedded_once() -> None:
    """Test that a cached text is not re-embedded and each distinct miss is embedded once.

    **Why this test is important:**
      - Re-ingesting a document (a redelivery, a re-sync, a shared footer) re-embeds the same
        texts; each one is a paid model call.

    **What it tests:**
      - a batch with a repeated text embeds each distinct text once and stores it with the TTL
      - a second batch with those texts makes no provider call and returns the same vectors,
        with ``token_count`` 0
      - hits and misses are counted in ``embedding_cache_requests_total``
    """
    inner, cache = _inner(), _cache()
    metrics = MagicMock(spec=MetricsProvider)
    counter = MagicMock(spec=MetricCounter)
    metrics.counter.return_value = counter
    cached = CachedEmbedding(inner, cache, ttl_seconds=60, version="v1", metrics=metrics)

    first = await cached.embed_batch(["ab", "ab", "cde"])
    second = await cached.embed_batch(["cde", "ab"])

    assert inner.embed_batch.await_count == 1
    assert inner.embed_batch.await_args.args[0] == ["ab", "cde"]
    assert {call.kwargs["ttl_seconds"] for call in cache.set.await_args_list} == {60}
    assert [r.embedding for r in first] == [[2.0, 0.5], [2.0, 0.5], [3.0, 0.5]]
    assert [(r.embedding, r.token_count) for r in second] == [
        ([3.0, 0.5], 0),
        ([2.0, 0.5], 0),
    ]
    counter.inc.assert_any_call(3, result="miss")
    counter.inc.assert_any_call(2, result="hit")
    assert (cached.model_name(), cached.dimension()) == ("m", 2)


@pytest.mark.asyncio
async def test_key_separates_models_versions_and_input_types() -> None:
    """Test that a vector is never shared across models, versions or input types.

    **Why this test is important:**
      - A vector from another model (or another prompt prefix, or a query prompt) in a
        document's slot puts a foreign-space vector into the index.

    **What it tests:**
      - over one shared cache, a second model, a bumped version and a ``query`` input type
        each miss and call their own provider
      - the cache keys never contain the text
    """
    cache = _cache()
    a, b = _inner("a"), _inner("b")
    await CachedEmbedding(a, cache, ttl_seconds=60, version="v1").embed("secret text")
    await CachedEmbedding(b, cache, ttl_seconds=60, version="v1").embed("secret text")
    await CachedEmbedding(a, cache, ttl_seconds=60, version="v2").embed("secret text")
    await CachedEmbedding(a, cache, ttl_seconds=60, version="v1").embed(
        "secret text", input_type="query"
    )

    assert (a.embed_batch.await_count, b.embed_batch.await_count) == (3, 1)
    keys = [call.args[0] for call in cache.set.await_args_list]
    assert len(set(keys)) == 4
    assert not any("secret" in k for k in keys)
