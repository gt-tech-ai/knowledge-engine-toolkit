"""Unit tests for ``SemanticCacheProxy``: read-through answers, fail-open.

Metrics and facts.
"""

from __future__ import annotations

import asyncio
import dataclasses
import json
from decimal import Decimal
from typing import TYPE_CHECKING, Any, cast
from unittest.mock import AsyncMock, MagicMock, call

import pytest
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from prometheus_client import CollectorRegistry

from techai_webutils.clients.decorators.token_accounting import (
    TokenAccountingOptions,
    TokenAccountingProxy,
)
from techai_webutils.clients.semantic_cache import reader as reader_module
from techai_webutils.clients.semantic_cache.proxy import (
    SemanticCacheBinding,
    SemanticCacheProxy,
    SemanticCacheProxyOptions,
)
from techai_webutils.clients.semantic_cache.stub import StubSemanticCache
from techai_webutils.core.errors import AppError, ErrorCode
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
    CacheLookup,
    CacheNamespace,
    SemanticCache,
    served_from_cache,
)
from techai_webutils.core.interfaces.token_ledger import (
    BudgetDecision,
    TokenLedger,
    UsageScope,
)
from techai_webutils.foundation.metrics.metrics import PrometheusMetricsProvider
from techai_webutils.foundation.tokens.cost import PriceTable
from techai_webutils.foundation.tokens.policy import BudgetPolicy

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_EXPECTED_CALL_COUNT = 2

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
    """Encode a response as JSON (the app's binding).

    Returns:
        A response as JSON (the app's binding).

    """
    response = cast("LLMResponse", result)
    return json.dumps({"text": response.content, "model": response.model}).encode()


def _decode(payload: bytes) -> object:
    """Decode a cached payload into a zero-token response.

    Returns:
        A cached payload into a zero-token response.

    """
    body = json.loads(payload)
    return LLMResponse(
        content=body["text"],
        model=body["model"],
        input_tokens=0,
        output_tokens=0,
        finish_reason="cache_hit",
    )


def _encode_text(text: str, usage: StreamUsage | None) -> bytes:
    """Encode a streamed answer in the unary format.

    Returns:
        A streamed answer in the unary format.

    """
    return json.dumps({"text": text, "model": usage.model if usage else ""}).encode()


def _decode_text(payload: bytes) -> tuple[str, str]:
    """Return the cached text and model.

    Returns:
        The cached text and model.

    """
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
    """Return a cache whose ``lookup`` answers ``hit`` (or raises ``error``).

    Returns:
        A cache whose ``lookup`` answers ``hit`` (or raises ``error``).

    """
    cache = MagicMock(spec=SemanticCache)
    cache.lookup = AsyncMock(side_effect=error, return_value=hit)
    cache.store = AsyncMock()
    return cache


def _inner() -> MagicMock:
    """Return a provider answering ``_RESPONSE``.

    Returns:
        A provider answering ``_RESPONSE``.

    """
    inner = MagicMock(spec=LLMProvider)
    inner.complete = AsyncMock(return_value=_RESPONSE)
    return inner


def _proxy(inner: MagicMock, cache: SemanticCache, **kwargs: object) -> LLMProvider:
    """Wrap ``inner`` on the generation path.

    Returns:
        The proxy.

    """
    options: dict[str, Any] = {
        "min_similarity": 0.95,
        "ttl_seconds": 600,
        "enabled": True,
        "path": "generation",
    }
    options.update(kwargs)
    return cast(
        "LLMProvider",
        SemanticCacheProxy(
            inner, cache, _BINDING, options=SemanticCacheProxyOptions(**options)
        ),
    )


def _metrics() -> tuple[MagicMock, dict[str, MagicMock]]:
    """Return a metrics provider and the counters it hands out, by name.

    Returns:
        A metrics provider and the counters it hands out, by name.

    """
    counters: dict[str, MagicMock] = {}
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(
        side_effect=lambda name, *_a: counters.setdefault(name, MagicMock())
    )
    return metrics, counters


def _increments(counter: MagicMock) -> list[Any]:
    """Return ``counter``'s non-zero ``inc`` calls.

    (dropping the 0 that creates a series).

    Returns:
        ``counter``'s non-zero ``inc`` calls.

    """
    return [c for c in counter.inc.call_args_list if c.args[0]]


@pytest.mark.asyncio
async def test_semantic_cache_proxy_hit_short_circuits_wrapped() -> None:
    """Test that a hit returns the decoded answer without calling the model.

    **Why this test is important:**
      - The saving exists only if the model is never called on a hit.

    **What it tests:**
      - the decoded cached answer is returned; the inner ``complete`` is never awaited
      - ``served_from_cache()`` is set and ``on_hit`` receives the hit, its ``tier``
        stamped from its similarity (``semantic`` below 1.0) when the backend left it
        blank
    """
    inner, on_hit = _inner(), AsyncMock(return_value=None)

    response = await _proxy(inner, _cache(_HIT), on_hit=on_hit).complete(_MESSAGES)

    assert (response.content, response.finish_reason, response.input_tokens) == (
        "30 days",
        "cache_hit",
        0,
    )
    inner.complete.assert_not_awaited()
    assert served_from_cache()
    assert on_hit.await_args is not None
    assert on_hit.await_args.args[3] == dataclasses.replace(_HIT, tier="semantic")


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
      - ``lookup`` raising → the model answers; ``store`` raising → the answer still
        returns
    """
    inner, cache = _inner(), _cache(error=RuntimeError("redis down"))
    cache.store = AsyncMock(side_effect=RuntimeError("redis down"))

    response = await _proxy(inner, cache).complete(_MESSAGES)

    assert response is _RESPONSE


@pytest.mark.asyncio
async def test_semantic_cache_proxy_passthrough_for_non_bound_methods() -> None:
    """Test that unbound methods.

    A disabled proxy and the stub cache pass straight through.

    **Why this test is important:**
      - Off must mean the uncached graph, byte for byte.

    **What it tests:**
      - ``model_name`` forwards; ``enabled=False`` and a ``StubSemanticCache`` never look
        up
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
    """Test that lookups, hits, misses and savings are counted per org.

    Path and workspace.

    **Why this test is important:**
      - The saving is the number the cache is judged by, and a dashboard's hit rate is
        hits over
        lookups, read per org: a counter without ``org_id`` can't be shown to a tenant.

    **What it tests:**
      - a hit increments ``semantic_cache_lookups_total`` and
        ``semantic_cache_hits_total`` and adds the ``saving`` to
        ``semantic_cache_saving_usd_total``; a miss increments lookups and
        ``semantic_cache_misses_total`` — each labelled with the ``org_id``
        ``fact_dimensions`` returns
      - every cache counter is registered with the ``org_id``, ``path`` and ``workspace``
        labels; the hit counter adds the serving ``tier``
    """
    metrics, counters = _metrics()
    org = {"fact_dimensions": lambda: {"org_id": "o1", "team": "t1"}}

    await _proxy(
        _inner(),
        _cache(_HIT),
        metrics=metrics,
        saving=lambda _hit: Decimal("0.002"),
        **org,
    ).complete(_MESSAGES)
    await _proxy(_inner(), _cache(), metrics=metrics, **org).complete(_MESSAGES)

    labels = {"org_id": "o1", "path": "generation", "workspace": "ws-1"}
    assert _increments(counters["semantic_cache_hits_total"]) == [
        call(1.0, **labels, tier="semantic")
    ]
    assert _increments(counters["semantic_cache_saving_usd_total"]) == [
        call(0.002, **labels)
    ]
    assert _increments(counters["semantic_cache_misses_total"]) == [call(1.0, **labels)]
    assert counters["semantic_cache_lookups_total"].inc.call_count == _EXPECTED_CALL_COUNT
    counters["semantic_cache_lookups_total"].inc.assert_called_with(1.0, **labels)
    registered = {call.args[0]: call.args[2] for call in metrics.counter.call_args_list}
    for name in (
        "semantic_cache_lookups_total",
        "semantic_cache_misses_total",
        "semantic_cache_saving_usd_total",
    ):
        assert registered[name] == ["org_id", "path", "workspace"]
    assert registered["semantic_cache_hits_total"] == [
        "org_id",
        "path",
        "workspace",
        "tier",
    ]


@pytest.mark.asyncio
async def test_semantic_cache_proxy_counts_without_an_org() -> None:
    """Test that a lookup with no ``fact_dimensions`` is still counted.

    Under an empty ``org_id``.

    **Why this test is important:**
      - A caller that never wired fact dimensions must not lose its cache metrics.

    **What it tests:**
      - a miss with no ``fact_dimensions`` increments lookups and misses with
        ``org_id=""``
    """
    metrics, counters = _metrics()

    await _proxy(_inner(), _cache(), metrics=metrics).complete(_MESSAGES)

    labels = {"org_id": "", "path": "generation", "workspace": "ws-1"}
    assert _increments(counters["semantic_cache_misses_total"]) == [call(1.0, **labels)]
    counters["semantic_cache_lookups_total"].inc.assert_called_once_with(1.0, **labels)


@pytest.mark.asyncio
async def test_semantic_cache_proxy_creates_the_hit_series_on_the_first_miss() -> None:
    """Test that a label set's first miss leaves its hit series in the registry at 0.

    **Why this test is important:**
      - Prometheus' ``increase()`` measures growth between samples, so a counter series
        whose first sample is already 1 reads as no change: without a 0 sample the first
        cache
            hit of
        a workspace never reaches a hit-rate panel.

    **What it tests:**
      - after one miss, ``semantic_cache_hits_total`` (for every tier) and
        ``semantic_cache_saving_usd_total`` exist at 0 for the lookup's
        ``org_id``/``path``/``workspace``; misses read 1
      - a later hit through the same proxy raises the hit series to 1
    """
    registry = CollectorRegistry()
    org = {"fact_dimensions": lambda: {"org_id": "o1"}}
    cache = _cache()
    proxy = _proxy(_inner(), cache, metrics=PrometheusMetricsProvider(registry), **org)
    labels = {"org_id": "o1", "path": "generation", "workspace": "ws-1"}

    await proxy.complete(_MESSAGES)

    for tier in ("exact", "semantic", "verified"):
        assert registry.get_sample_value(
            "semantic_cache_hits_total", {**labels, "tier": tier}
        ) == pytest.approx(0.0)
    assert registry.get_sample_value(
        "semantic_cache_saving_usd_total", labels
    ) == pytest.approx(0.0)
    assert registry.get_sample_value(
        "semantic_cache_misses_total", labels
    ) == pytest.approx(1.0)

    cache.lookup = AsyncMock(return_value=_HIT)
    await proxy.complete(_MESSAGES)

    assert registry.get_sample_value(
        "semantic_cache_hits_total", {**labels, "tier": "semantic"}
    ) == pytest.approx(1.0)


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
    """Yield ``items``, then raise when ``fail``.

    Yields:
        ``items``, then raise when ``fail``.

    Raises:
        RuntimeError: Dropped.

    """
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
    """Test that a streamed hit replays the cached answer.

    And never opens the model's stream.

    **Why this test is important:**
      - Chat streams; caching only the unary path would miss the main saving.

    **What it tests:**
      - the replayed text equals the cached text; the terminal ``StreamUsage`` reports
        zero tokens with ``finish_reason="cache_hit"``; ``stream`` yields text only
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
async def test_stream_hit_is_handed_to_on_hit_as_a_response() -> None:
    """Test that a streamed hit reaches the hit hook as an answer.

    And serves its replacement.

    **Why this test is important:**
      - The hook is the audit of a hit; handed the replay iterator it can't read the
        answer, so streamed hits (the chat path) would go unaudited and unscanned.

    **What it tests:**
      - ``on_hit`` receives an ``LLMResponse`` with the cached text and model
      - the answer the hook returns (a redaction) is what the stream replays
    """
    inner = _inner()
    inner.stream_with_usage = AsyncMock()
    redacted = LLMResponse(
        content="[redacted]",
        model="sonnet",
        input_tokens=0,
        output_tokens=0,
        finish_reason="cache_hit",
    )
    on_hit = AsyncMock(return_value=redacted)

    stream = await _proxy(inner, _cache(_HIT), on_hit=on_hit).stream(_MESSAGES)
    text = "".join([item async for item in stream])

    assert on_hit.await_args is not None
    handed = on_hit.await_args.args[2]
    assert isinstance(handed, LLMResponse)
    assert (handed.content, handed.model) == ("30 days", "sonnet")
    assert text == "[redacted]"


@pytest.mark.asyncio
@pytest.mark.parametrize("code", [ErrorCode.FORBIDDEN, ErrorCode.UNAVAILABLE])
async def test_hit_hook_coded_error_refuses_the_hit(code: ErrorCode) -> None:
    """Test that any coded error from the hit hook refuses the hit instead.

    Of being swallowed.

    **Why this test is important:**
      - The audit blocks a hit the safety policy forbids (``FORBIDDEN``) and, when
        enforced, refuses one it could not scan or record (``UNAVAILABLE``); swallowing
        either would serve an answer the live path refuses.

    **What it tests:**
      - ``complete`` raises the hook's coded error; the call is not marked served from
        cache
    """
    refusing = AsyncMock(side_effect=AppError(code, "refused"))

    with pytest.raises(AppError) as caught:
        await _proxy(_inner(), _cache(_HIT), on_hit=refusing).complete(_MESSAGES)

    assert caught.value.code is code
    assert not served_from_cache()


@pytest.mark.asyncio
async def test_hit_hook_uncoded_failure_serves_the_hit() -> None:
    """Test that an uncoded hit-hook failure is swallowed, counted and the hit served.

    **Why this test is important:**
      - A bug in the hook must not turn every hit into an error; the cache stays
        fail-open.

    **What it tests:**
      - ``complete`` returns the cached answer and counts ``op="on_hit"``
    """
    metrics, counters = _metrics()
    broken = AsyncMock(side_effect=RuntimeError("bug"))

    response = await _proxy(
        _inner(), _cache(_HIT), on_hit=broken, metrics=metrics
    ).complete(_MESSAGES)

    assert response.content == "30 days"
    counters["semantic_cache_proxy_errors_total"].inc.assert_any_call(1.0, op="on_hit")


@pytest.mark.asyncio
async def test_stream_hit_ignores_a_non_response_replacement() -> None:
    """Test that a stream hit whose hook returns a non-answer replays the cached text.

    Counted.

    **Why this test is important:**
      - A replay can only stream text; a replacement it can't read must be visible as a
        hook failure, not silently dropped.

    **What it tests:**
      - the cached text is replayed and ``op="on_hit"`` is counted
    """
    metrics, counters = _metrics()
    inner = _inner()
    inner.stream_with_usage = AsyncMock()
    odd = AsyncMock(return_value="not a response")

    stream = await _proxy(inner, _cache(_HIT), on_hit=odd, metrics=metrics).stream(
        _MESSAGES
    )
    text = "".join([item async for item in stream])

    assert text == "30 days"
    counters["semantic_cache_proxy_errors_total"].inc.assert_any_call(1.0, op="on_hit")


@pytest.mark.asyncio
async def test_stream_miss_stores_after_full_consumption() -> None:
    """Test that a streamed miss is stored once, after the stream is fully consumed.

    **Why this test is important:**
      - Storing before the end would cache a truncated answer.

    **What it tests:**
      - the items pass through unchanged; ``store`` gets the joined text in the unary
        format
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
      - The two decorators meet only through ``served_from_cache()``; a hit billed at the
        cached answer's tokens would charge the tenant for a call that never happened.

    **What it tests:**
      - a hit records one usage with ``operation="generate.cache_hit"`` and zero tokens
      - a following miss records the model's real tokens
    """
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
                options=TokenAccountingOptions(
                    ledger=ledger,
                    scope=lambda: UsageScope(org_id="org-1"),
                    prices=PriceTable(prices={}, version="v1"),
                    policy=BudgetPolicy(),
                    step="generate",
                ),
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


@pytest.mark.asyncio
async def test_semantic_cache_proxy_reads_through_an_injected_tier_chain(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Test that a proxy given a ``CacheLookup`` reads through it.

    And reports the serving tier.

    **Why this test is important:**
      - The tier list is configuration; a proxy that ignored it would keep serving by one
        threshold, and the Cache screen reads hits and spans by tier.

    **What it tests:**
      - the injected lookup is awaited (the backend's ``lookup`` is not) and its exact hit
        is served without calling the model
      - ``semantic_cache_hits_total`` increments with ``tier="exact"`` and the
        ``semantic_cache.lookup`` span carries ``semantic_cache.tier="exact"``
    """
    exporter = InMemorySpanExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    monkeypatch.setattr(reader_module, "_TRACER", provider.get_tracer("test"))
    metrics, counters = _metrics()
    lookup = MagicMock(spec=CacheLookup)
    lookup.lookup = AsyncMock(
        return_value=dataclasses.replace(_HIT, similarity=1.0, tier="exact")
    )
    inner, cache = _inner(), _cache()

    await _proxy(inner, cache, metrics=metrics, lookup=lookup).complete(_MESSAGES)

    inner.complete.assert_not_awaited()
    cache.lookup.assert_not_awaited()
    hits = _increments(counters["semantic_cache_hits_total"])
    assert [c.kwargs["tier"] for c in hits] == ["exact"]
    (span,) = exporter.get_finished_spans()
    assert span.attributes is not None
    assert span.attributes["semantic_cache.tier"] == "exact"
