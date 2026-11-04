"""Unit tests for ``SemanticCacheReader``: one read-through step a workflow can drive by hand.

The tier chain and the backend are mocks (``CacheLookup`` / ``SemanticCache``), so each test pins
what the reader adds around them: metrics, facts, the hit hook, the served flag and the write.
"""

from __future__ import annotations

from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.semantic_cache.reader import CacheRead, SemanticCacheReader
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.core.interfaces.metrics import MetricsProvider
from techai_webutils.core.interfaces.semantic_cache import (
    CachedAnswer,
    CacheLookup,
    CacheNamespace,
    SemanticCache,
    served_from_cache,
    set_served_from_cache,
)

_NS = CacheNamespace("ws-1", "m", "v1", "restricted:v7")
_HIT = CachedAnswer(
    payload=b"answer", similarity=1.0, source_query="q", embedding_model="m", tier="exact"
)


def _metrics() -> tuple[MagicMock, dict[str, MagicMock]]:
    """Return a metrics provider and the counters it hands out, by name."""
    counters: dict[str, MagicMock] = {}
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(
        side_effect=lambda name, *_a: counters.setdefault(name, MagicMock())
    )
    return metrics, counters


def _reader(
    hit: CachedAnswer | None,
    **kwargs: Any,  # noqa: ANN401
) -> tuple[SemanticCacheReader, MagicMock, MagicMock]:
    """Return a reader over a lookup answering ``hit``, plus the lookup and backend mocks."""
    lookup = MagicMock(spec=CacheLookup)
    lookup.lookup = AsyncMock(return_value=hit)
    cache = MagicMock(spec=SemanticCache)
    cache.store = AsyncMock()
    options: dict[str, Any] = {"ttl_seconds": 600, "path": "answer"}
    options.update(kwargs)
    return SemanticCacheReader(cache, lookup, **options), lookup, cache


@pytest.mark.asyncio
async def test_read_counts_the_hit_by_tier_and_forwards_the_similarity() -> None:
    """Test that a read reports the serving tier and passes a per-call threshold through.

    **Why this test is important:**
      - The Cache screen and alerts read hits by tier; a per-workspace similarity must reach
        the tiers, or a workspace's 1.0 ("exact only") setting would be ignored.

    **What it tests:**
      - ``read`` returns the hit in a ``CacheRead`` and awaits the lookup with
        ``min_similarity=1.0``
      - ``semantic_cache_hits_total`` increments once with ``tier="exact"``
      - one ``cache_lookups`` fact is published with outcome ``hit``
    """
    metrics, counters = _metrics()
    facts = MagicMock(spec=FactPublisher)
    reader, lookup, _ = _reader(
        _HIT,
        metrics=metrics,
        facts=facts,
        fact_dimensions=lambda: {"org_id": "org-1"},
    )

    read = await reader.read("q", _NS, min_similarity=1.0)

    assert read == CacheRead(query="q", namespace=_NS, hit=_HIT)
    lookup.lookup.assert_awaited_once_with("q", _NS, min_similarity=1.0)
    hits = [
        c for c in counters["semantic_cache_hits_total"].inc.call_args_list if c.args[0]
    ]
    assert len(hits) == 1
    assert hits[0].kwargs["tier"] == "exact"
    (published,) = facts.publish.call_args.args[0]
    assert published.dims["outcome"] == "hit"


@pytest.mark.asyncio
async def test_serve_runs_the_hook_and_marks_the_call_served() -> None:
    """Test that serving a hit runs the hook, may replace the answer, and sets the flag.

    **Why this test is important:**
      - A workflow-level cache must audit every hit exactly like the model-level one, and the
        accounting band reads ``served_from_cache()`` to bill a hit as zero tokens.

    **What it tests:**
      - the hook receives the call's args, kwargs, answer and hit; its return replaces the answer
      - ``served_from_cache()`` is ``True`` after ``serve``; a coded hook error propagates
    """
    hook = AsyncMock(return_value="redacted")
    reader, _, _ = _reader(_HIT, on_hit=hook)
    read = await reader.read("q", _NS)
    set_served_from_cache(value=False)

    served = await reader.serve(read, ("q",), {"k": 1}, "answer")

    assert served == "redacted"
    hook.assert_awaited_once_with(("q",), {"k": 1}, "answer", _HIT)
    assert served_from_cache() is True

    refusing, _, _ = _reader(
        _HIT, on_hit=AsyncMock(side_effect=AppError(ErrorCode.FORBIDDEN, "blocked"))
    )
    with pytest.raises(AppError):
        await refusing.serve(await refusing.read("q", _NS), (), {}, "answer")


@pytest.mark.asyncio
async def test_write_uses_the_per_call_ttl_and_fails_open() -> None:
    """Test that a write stores under the read's key with a per-call TTL, and never raises.

    **Why this test is important:**
      - TTL is a per-workspace setting; and a failed write must never fail the answer the
        user already has.

    **What it tests:**
      - ``write`` stores ``payload`` under the read's query and namespace with ``ttl_seconds=60``
      - without an override it uses the reader's 600; a raising store is swallowed and counted
    """
    metrics, counters = _metrics()
    reader, _, cache = _reader(None, metrics=metrics)
    read = await reader.read("q", _NS)

    await reader.write(read, b"payload", ttl_seconds=60)
    await reader.write(read, b"payload")

    assert cache.store.await_args_list[0].args == ("q", b"payload", _NS)
    assert cache.store.await_args_list[0].kwargs == {"ttl_seconds": 60}
    assert cache.store.await_args_list[1].kwargs == {"ttl_seconds": 600}

    cache.store = AsyncMock(side_effect=RuntimeError("down"))
    await reader.write(read, b"payload")
    counters["semantic_cache_proxy_errors_total"].inc.assert_called_with(1.0, op="store")
