"""Unit tests for ``SemanticCacheProxy``: read-through answers, fail-open, metrics and facts."""

from __future__ import annotations

import asyncio
import json
from decimal import Decimal
from typing import TYPE_CHECKING, Any, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.decorators.semantic_cache_proxy import (
    SemanticCacheBinding,
    SemanticCacheProxy,
)
from techai_webutils.clients.semantic_cache.stub import StubSemanticCache
from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.core.interfaces.llm import (
    LLMMessage,
    LLMProvider,
    LLMResponse,
    StreamUsage,
)
from techai_webutils.core.interfaces.metrics import MetricsProvider
from techai_webutils.core.interfaces.semantic_cache import (
    CachedAnswer,
    CacheNamespace,
    SemanticCache,
    served_from_cache,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_NS = CacheNamespace(
    workspace_id="ws-1", embedding_model="titan", embedding_version="1024"
)
_MESSAGES = [LLMMessage(role="user", content="what is the refund window")]
_RESPONSE = LLMResponse(
    content="30 days",
    model="sonnet",
    input_tokens=9,
    output_tokens=3,
    finish_reason="stop",
)


def _encode(result: object) -> bytes:
    """Encode a response as JSON (the app's binding)."""
    response = cast("LLMResponse", result)
    return json.dumps({"text": response.content, "model": response.model}).encode()


def _decode(payload: bytes) -> object:
    """Decode a cached payload into a zero-token response."""
    body = json.loads(payload)
    return LLMResponse(
        content=body["text"],
        model=body["model"],
        input_tokens=0,
        output_tokens=0,
        finish_reason="cache_hit",
    )


def _encode_text(text: str, usage: StreamUsage | None) -> bytes:
    """Encode a streamed answer in the unary format."""
    return json.dumps({"text": text, "model": usage.model if usage else ""}).encode()


def _decode_text(payload: bytes) -> tuple[str, str]:
    """Return the cached text and model."""
    body = json.loads(payload)
    return body["text"], body["model"]


_BINDING = SemanticCacheBinding(
    method="complete",
    make_query=lambda args, _kwargs: cast("list[LLMMessage]", args[0])[-1].content,
    make_namespace=lambda _args, _kwargs: _NS,
    encode=_encode,
    decode=_decode,
    stream_method="stream_with_usage",
    encode_text=_encode_text,
    decode_text=_decode_text,
)
_HIT = CachedAnswer(
    payload=_encode(_RESPONSE),
    similarity=0.97,
    source_query="refund window?",
    embedding_model="titan",
)


def _cache(hit: CachedAnswer | None = None, error: Exception | None = None) -> MagicMock:
    """Return a cache whose ``lookup`` answers ``hit`` (or raises ``error``)."""
    cache = MagicMock(spec=SemanticCache)
    cache.lookup = AsyncMock(side_effect=error, return_value=hit)
    cache.store = AsyncMock()
    return cache


def _inner() -> MagicMock:
    """Return a provider answering ``_RESPONSE``."""
    inner = MagicMock(spec=LLMProvider)
    inner.complete = AsyncMock(return_value=_RESPONSE)
    return inner


def _proxy(inner: MagicMock, cache: SemanticCache, **kwargs: Any) -> LLMProvider:  # noqa: ANN401
    """Wrap ``inner`` on the generation path."""
    options: dict[str, Any] = {
        "min_similarity": 0.95,
        "ttl_seconds": 600,
        "enabled": True,
        "path": "generation",
    }
    options.update(kwargs)
    return cast("LLMProvider", SemanticCacheProxy(inner, cache, _BINDING, **options))


def _metrics() -> tuple[MagicMock, dict[str, MagicMock]]:
    """Return a metrics provider and the counters it hands out, by name."""
    counters: dict[str, MagicMock] = {}
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(
        side_effect=lambda name, *_a: counters.setdefault(name, MagicMock())
    )
    return metrics, counters


@pytest.mark.asyncio
async def test_semantic_cache_proxy_hit_short_circuits_wrapped() -> None:
    """Test that a hit returns the decoded answer without calling the model.

    **Why this test is important:**
      - The saving exists only if the model is never called on a hit.

    **What it tests:**
      - the decoded cached answer is returned; the inner ``complete`` is never awaited
      - ``served_from_cache()`` is set and ``on_hit`` receives the hit
    """
    inner, on_hit = _inner(), AsyncMock()

    response = await _proxy(inner, _cache(_HIT), on_hit=on_hit).complete(_MESSAGES)

    assert (response.content, response.finish_reason, response.input_tokens) == (
        "30 days",
        "cache_hit",
        0,
    )
    inner.complete.assert_not_awaited()
    assert served_from_cache()
    assert on_hit.await_args is not None
    assert on_hit.await_args.args[3] is _HIT


@pytest.mark.asyncio
async def test_semantic_cache_proxy_miss_calls_wrapped_then_stores() -> None:
    """Test that a miss serves from the model and writes the answer through.

    **Why this test is important:**
      - Without the write-through the cache never warms.

    **What it tests:**
      - the model's response is returned unchanged
      - ``store`` receives the query, the encoded response, the namespace and the TTL
    """
    inner, cache = _inner(), _cache()

    response = await _proxy(inner, cache).complete(_MESSAGES)

    assert response is _RESPONSE
    assert not served_from_cache()
    cache.store.assert_awaited_once_with(
        "what is the refund window", _encode(_RESPONSE), _NS, ttl_seconds=600
    )


@pytest.mark.asyncio
async def test_semantic_cache_proxy_fails_open() -> None:
    """Test that a cache that raises never fails or changes the answer.

    **Why this test is important:**
      - The cache is an optimisation; its outage must be invisible to the user.

    **What it tests:**
      - ``lookup`` raising → the model answers; ``store`` raising → the answer still returns
    """
    inner, cache = _inner(), _cache(error=RuntimeError("redis down"))
    cache.store = AsyncMock(side_effect=RuntimeError("redis down"))

    response = await _proxy(inner, cache).complete(_MESSAGES)

    assert response is _RESPONSE


@pytest.mark.asyncio
async def test_semantic_cache_proxy_passthrough_for_non_bound_methods() -> None:
    """Test that unbound methods, a disabled proxy and the stub cache pass straight through.

    **Why this test is important:**
      - Off must mean the uncached graph, byte for byte.

    **What it tests:**
      - ``model_name`` forwards; ``enabled=False`` and a ``StubSemanticCache`` never look up
    """
    inner, cache = _inner(), _cache(_HIT)
    inner.model_name = MagicMock(return_value="sonnet")

    name = _proxy(inner, cache).model_name()
    disabled = await _proxy(inner, cache, enabled=False).complete(_MESSAGES)
    stubbed = await _proxy(inner, StubSemanticCache()).complete(_MESSAGES)

    assert (name, disabled, stubbed) == ("sonnet", _RESPONSE, _RESPONSE)
    cache.lookup.assert_not_awaited()


@pytest.mark.asyncio
async def test_semantic_cache_proxy_emits_hit_and_saving_metrics() -> None:
    """Test that hits, misses and savings are counted per path and workspace.

    **Why this test is important:**
      - The saving is the number the cache is judged by.

    **What it tests:**
      - a hit increments ``semantic_cache_hits_total`` and adds the ``saving`` to
        ``semantic_cache_saving_usd_total``; a miss increments ``semantic_cache_misses_total``
    """
    metrics, counters = _metrics()

    await _proxy(
        _inner(), _cache(_HIT), metrics=metrics, saving=lambda _hit: Decimal("0.002")
    ).complete(_MESSAGES)
    await _proxy(_inner(), _cache(), metrics=metrics).complete(_MESSAGES)

    counters["semantic_cache_hits_total"].inc.assert_called_once_with(
        1.0, path="generation", workspace="ws-1"
    )
    counters["semantic_cache_saving_usd_total"].inc.assert_called_once_with(
        0.002, path="generation", workspace="ws-1"
    )
    counters["semantic_cache_misses_total"].inc.assert_called_once_with(
        1.0, path="generation", workspace="ws-1"
    )


@pytest.mark.asyncio
async def test_semantic_cache_proxy_emits_cache_lookups_fact() -> None:
    """Test that each lookup publishes one ``cache_lookups`` fact.

    **Why this test is important:**
      - The similarity distribution and per-workspace savings live only in these facts.

    **What it tests:**
      - a hit at 0.97 publishes outcome ``hit``, bin ``0.95`` and the saving; a miss
        publishes outcome ``miss``
    """
    facts = MagicMock(spec=FactPublisher)
    options: dict[str, Any] = {
        "facts": facts,
        "fact_dimensions": lambda: {"org_id": "org-1"},
        "saving": lambda _hit: Decimal("0.002"),
    }

    await _proxy(_inner(), _cache(_HIT), **options).complete(_MESSAGES)
    await _proxy(_inner(), _cache(), **options).complete(_MESSAGES)

    hit, miss = (call.args[0][0] for call in facts.publish.call_args_list)
    assert (hit.cube, hit.org_id) == ("cache_lookups", "org-1")
    assert (hit.dims["outcome"], hit.dims["similarity_bin"], hit.dims["workspace"]) == (
        "hit",
        "0.95",
        "ws-1",
    )
    assert hit.measures == {"lookups": 1.0, "saving_usd": 0.002}
    assert miss.dims["outcome"] == "miss"


@pytest.mark.asyncio
async def test_fact_publisher_error_never_turns_hit_into_miss() -> None:
    """Test that a failing fact publisher leaves a hit a hit.

    **Why this test is important:**
      - Telemetry must never change what the user gets, or what it costs.

    **What it tests:**
      - with ``publish`` raising, a hit still short-circuits the model
    """
    facts = MagicMock(spec=FactPublisher)
    facts.publish = MagicMock(side_effect=RuntimeError("queue full"))
    inner = _inner()

    response = await _proxy(
        inner, _cache(_HIT), facts=facts, fact_dimensions=lambda: {"org_id": "o"}
    ).complete(_MESSAGES)

    assert response.finish_reason == "cache_hit"
    inner.complete.assert_not_awaited()


async def _items(
    *items: str | StreamUsage, fail: bool = False
) -> AsyncIterator[str | StreamUsage]:
    """Yield ``items``, then raise when ``fail``."""
    for item in items:
        await asyncio.sleep(0)
        yield item
    if fail:
        msg = "dropped"
        raise RuntimeError(msg)


_USAGE = StreamUsage(
    model="sonnet", input_tokens=9, output_tokens=3, finish_reason="stop"
)


@pytest.mark.asyncio
async def test_stream_hit_replays_cached_text_without_opening_inner() -> None:
    """Test that a streamed hit replays the cached answer and never opens the model's stream.

    **Why this test is important:**
      - Chat streams; caching only the unary path would miss the main saving.

    **What it tests:**
      - the replayed text equals the cached text; the terminal ``StreamUsage`` reports zero
        tokens with ``finish_reason="cache_hit"``; ``stream`` yields text only
    """
    inner = _inner()
    inner.stream_with_usage = AsyncMock()
    proxy = _proxy(inner, _cache(_HIT))

    items = [item async for item in await proxy.stream_with_usage(_MESSAGES)]
    text = [item async for item in await proxy.stream(_MESSAGES)]

    assert "".join(i for i in items if isinstance(i, str)) == "30 days"
    assert items[-1] == StreamUsage(
        model="sonnet", input_tokens=0, output_tokens=0, finish_reason="cache_hit"
    )
    assert "".join(text) == "30 days"
    inner.stream_with_usage.assert_not_awaited()


@pytest.mark.asyncio
async def test_stream_miss_stores_after_full_consumption() -> None:
    """Test that a streamed miss is stored once, after the stream is fully consumed.

    **Why this test is important:**
      - Storing before the end would cache a truncated answer.

    **What it tests:**
      - the items pass through unchanged; ``store`` gets the joined text in the unary format
    """
    inner, cache = _inner(), _cache()
    inner.stream_with_usage = AsyncMock(
        side_effect=lambda *_a, **_k: _items("30 ", "days", _USAGE)
    )

    items = [
        item async for item in await _proxy(inner, cache).stream_with_usage(_MESSAGES)
    ]

    assert items == ["30 ", "days", _USAGE]
    cache.store.assert_awaited_once_with(
        "what is the refund window", _encode(_RESPONSE), _NS, ttl_seconds=600
    )


@pytest.mark.asyncio
async def test_abandoned_stream_stores_nothing() -> None:
    """Test that a stream that fails or is abandoned is never cached.

    **Why this test is important:**
      - A partial answer replayed to the next user is worse than no cache.

    **What it tests:**
      - a stream that raises mid-way and a stream closed after one item both store nothing
    """
    inner, cache = _inner(), _cache()
    inner.stream_with_usage = AsyncMock(
        side_effect=lambda *_a, **_k: _items("30 ", fail=True)
    )
    proxy = _proxy(inner, cache)

    async def drain() -> None:
        async for _ in await proxy.stream_with_usage(_MESSAGES):
            pass

    with pytest.raises(RuntimeError):
        await drain()
    inner.stream_with_usage = AsyncMock(
        side_effect=lambda *_a, **_k: _items("30 ", "days", _USAGE)
    )
    stream = cast("Any", await proxy.stream_with_usage(_MESSAGES))
    await anext(stream)
    await stream.aclose()

    cache.store.assert_not_awaited()


@pytest.mark.asyncio
async def test_stream_lookup_error_fails_open() -> None:
    """Test that a lookup error on the stream path serves the model's stream.

    **Why this test is important:**
      - The chat path must survive a cache outage like the unary one.

    **What it tests:**
      - ``lookup`` raising → the inner stream's items pass through unchanged
    """
    inner = _inner()
    inner.stream_with_usage = AsyncMock(
        side_effect=lambda *_a, **_k: _items("30 ", "days", _USAGE)
    )

    items = [
        item
        async for item in await _proxy(
            inner, _cache(error=RuntimeError("down"))
        ).stream_with_usage(_MESSAGES)
    ]

    assert items == ["30 ", "days", _USAGE]


@pytest.mark.asyncio
async def test_accounting_over_cache_records_a_hit_as_zero_tokens() -> None:
    """Test that token accounting outside the cache records a hit as one zero-token usage.

    **Why this test is important:**
      - The two decorators meet only through ``served_from_cache()``; a hit billed at the cached
        answer's tokens would charge the tenant for a call that never happened.

    **What it tests:**
      - a hit records one usage with ``operation="generate.cache_hit"`` and zero tokens
      - a following miss records the model's real tokens
    """
    from techai_webutils.clients.decorators.token_accounting import (
        TokenAccountingProxy,
    )
    from techai_webutils.clients.token_ledger.cost import PriceTable
    from techai_webutils.clients.token_ledger.policy import BudgetPolicy
    from techai_webutils.core.interfaces.token_ledger import (
        BudgetDecision,
        TokenLedger,
        UsageScope,
    )

    ledger = MagicMock(spec=TokenLedger)
    ledger.check_budget = AsyncMock(
        return_value=BudgetDecision(
            allowed=True,
            reason="no_limit",
            remaining_tokens=None,
            remaining_cost_usd=None,
        )
    )
    ledger.record = AsyncMock()
    cache = _cache(_HIT)

    def accounted() -> LLMProvider:
        return cast(
            "LLMProvider",
            TokenAccountingProxy(
                _proxy(_inner(), cache),
                ledger=ledger,
                scope=lambda: UsageScope(org_id="org-1"),
                prices=PriceTable(prices={}, version="v1"),
                policy=BudgetPolicy(),
                step="generate",
            ),
        )

    await accounted().complete(_MESSAGES)
    cache.lookup = AsyncMock(return_value=None)
    await accounted().complete(_MESSAGES)

    hit, miss = (call.args[0] for call in ledger.record.await_args_list)
    assert (hit.operation, hit.input_tokens, hit.output_tokens) == (
        "generate.cache_hit",
        0,
        0,
    )
    assert (miss.operation, miss.input_tokens, miss.output_tokens) == ("generate", 9, 3)
