"""``SemanticCacheProxy`` — a read-through semantic cache around one model method.

A method-aware ``__getattr__`` proxy: only the binding's ``method`` (unary) and, when set, its
``stream_method`` (``stream_with_usage``; ``stream`` is served by the same wrapper) are cached;
every other attribute passes through. Per bound call it:

1. resets ``served_from_cache()``, then derives the query and the ``CacheNamespace`` from the
   call's arguments (a ``None`` namespace passes through);
2. looks the query up inside a ``semantic_cache.lookup`` span (``hit``, ``similarity``);
3. **hit** — returns the decoded answer without calling the inner (a stream replays the cached
   text, then a zero-token ``StreamUsage`` with ``finish_reason="cache_hit"``), counts
   ``semantic_cache_hits_total`` and the injected ``saving``, awaits ``on_hit`` with the answer
   (an ``LLMResponse`` for a stream) before serving it, then sets ``served_from_cache()``. The
   hook may return a replacement answer (e.g. redacted; a stream replays only an
   ``LLMResponse`` replacement) and refuses the hit by raising any coded ``AppError`` (a
   safety block, or an enforced audit that could not scan or record), which propagates;
4. **miss** — calls the inner and writes the answer through with the TTL (a stream only once it
   was fully consumed without error, so a truncated answer is never cached).

Everything else the cache, the hook or the telemetry raise is logged, counted
(``semantic_cache_proxy_errors_total{op}``) and swallowed: apart from the hook's refusal or
replacement, the proxy can never fail or change the answer. ``enabled=False`` or a
``StubSemanticCache`` makes it a passthrough. Each lookup publishes one ``cache_lookups``
fact (outcome, similarity bin, model, workspace; lookups, saving).
"""

from __future__ import annotations

import functools
import uuid
from dataclasses import dataclass
from datetime import UTC, datetime
from decimal import Decimal
from typing import TYPE_CHECKING, cast

from opentelemetry import trace

from techai_webutils.clients.semantic_cache.stub import StubSemanticCache
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import LLMResponse, StreamUsage
from techai_webutils.core.interfaces.semantic_cache import set_served_from_cache
from techai_webutils.core.types.fact import Fact
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable, Mapping

    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.semantic_cache import (
        CachedAnswer,
        CacheNamespace,
        SemanticCache,
    )

    type CallArgs = tuple[tuple[object, ...], dict[str, object]]
    """A bound call's positional and keyword arguments."""
    type HitHook = Callable[
        [tuple[object, ...], dict[str, object], object, CachedAnswer],
        Awaitable[object | None],
    ]
    """Awaited before a hit is served with the call's args, kwargs, the decoded answer and the
    hit; returns the answer to serve instead, or ``None`` to serve it as is."""

CACHE_LOOKUPS_CUBE = "cache_lookups"
"""The analytics cube each lookup lands in."""

_REPLAY_CHUNK_CHARS = 64
"""Characters per chunk when a cached answer is replayed as a stream."""

_TRACER = trace.get_tracer(__name__)
"""Emits the ``semantic_cache.lookup`` span."""


@dataclass(frozen=True, slots=True)
class SemanticCacheBinding:
    """Which method is cached and how its arguments and results map to the cache."""

    method: str
    """The unary method cached (e.g. ``complete``)."""
    make_query: Callable[[tuple[object, ...], dict[str, object]], str]
    """Returns the text a call is keyed by."""
    make_namespace: Callable[
        [tuple[object, ...], dict[str, object]], CacheNamespace | None
    ]
    """Returns the call's namespace; ``None`` skips the cache for that call."""
    encode: Callable[[object], bytes]
    """Encodes the unary result for the cache."""
    decode: Callable[[bytes], object]
    """Decodes a cached payload into the unary result."""
    stream_method: str | None = None
    """The streamed method cached (``stream_with_usage``); ``None`` streams pass through."""
    encode_text: Callable[[str, StreamUsage | None], bytes] | None = None
    """Encodes a fully streamed answer (its text and terminal usage) for the cache."""
    decode_text: Callable[[bytes], tuple[str, str]] | None = None
    """Returns a cached payload's text and model, for a streamed replay."""


@dataclass(frozen=True, slots=True)
class _Lookup:
    """One bound call's cache key and what the lookup found."""

    query: str
    """The text the call is keyed by."""
    namespace: CacheNamespace
    """The call's namespace."""
    hit: CachedAnswer | None
    """The hit, or ``None`` for a miss (or a failed lookup)."""


class SemanticCacheProxy:
    """Serves repeat model calls from a semantic cache; fail-open."""

    def __init__(  # noqa: PLR0913 - the decorator's settings and collaborators, each injected
        self,
        inner: object,
        cache: SemanticCache,
        binding: SemanticCacheBinding,
        *,
        min_similarity: float,
        ttl_seconds: int,
        enabled: bool,
        path: str,
        metrics: MetricsProvider | None = None,
        facts: FactPublisher | None = None,
        fact_dimensions: Callable[[], Mapping[str, str]] | None = None,
        saving: Callable[[CachedAnswer], Decimal] | None = None,
        on_hit: HitHook | None = None,
        similarity_bins: tuple[float, ...] = (0.9, 0.95, 0.98, 1.0),
    ) -> None:
        """Wrap ``inner``'s bound methods with ``cache``.

        Args:
            inner: The provider or generator whose calls are cached.
            cache: The semantic cache.
            binding: Which methods are cached and how.
            min_similarity: The lowest similarity that counts as a hit.
            ttl_seconds: How long a stored answer lives.
            enabled: ``False`` makes the proxy a passthrough.
            path: The metric label for this placement (``generation``, ``retrieval``).
            metrics: Emits hit / miss / saving / error counters; ``None`` emits nothing.
            facts: Receives one ``cache_lookups`` fact per lookup; ``None`` publishes nothing.
            fact_dimensions: Returns the product dimensions (must include ``org_id``).
            saving: Returns the US dollars a hit avoided (``0`` without it).
            on_hit: Awaited before each hit is served (e.g. the audit); a ``FORBIDDEN`` raise
                refuses the hit, any other failure is swallowed.
            similarity_bins: The ascending lower edges a hit's similarity is binned by.

        """
        self._inner = inner
        self._cache = cache
        self._binding = binding
        self._min_similarity = min_similarity
        self._ttl = ttl_seconds
        self._active = enabled and not isinstance(cache, StubSemanticCache)
        self._path = path
        self._facts = facts
        self._fact_dimensions = fact_dimensions
        self._saving = saving
        self._on_hit = on_hit
        self._bins = similarity_bins
        self._logger = get_logger("semantic_cache_proxy")
        self._counters: dict[str, MetricCounter] = {}
        if metrics is not None:
            for name, help_text, labels in (
                (
                    "semantic_cache_hits_total",
                    "Semantic cache hits.",
                    ["path", "workspace"],
                ),
                (
                    "semantic_cache_misses_total",
                    "Semantic cache misses.",
                    ["path", "workspace"],
                ),
                (
                    "semantic_cache_saving_usd_total",
                    "US dollars avoided by hits.",
                    ["path", "workspace"],
                ),
                (
                    "semantic_cache_proxy_errors_total",
                    "Swallowed cache-proxy errors.",
                    ["op"],
                ),
            ):
                self._counters[name] = metrics.counter(name, help_text, labels)

    def __getattr__(self, name: str) -> object:
        """Return the inner attribute, cached when it is a bound method."""
        attr = getattr(self._inner, name)
        if not callable(attr) or not self._active:
            return attr
        stream_method = self._binding.stream_method
        if name == self._binding.method:
            return self._wrap_call(attr)
        if stream_method is not None and name in {stream_method, "stream"}:
            source = getattr(self._inner, stream_method)
            return self._wrap_stream(attr, source, keep_usage=name == stream_method)
        return attr

    def _count(self, name: str, value: float = 1.0, **labels: str) -> None:
        """Increment the counter ``name`` (no-op without metrics)."""
        counter = self._counters.get(name)
        if counter is not None:
            counter.inc(value, **labels)

    def _failed(self, op: str, error: BaseException) -> None:
        """Log and count a swallowed failure of ``op``."""
        self._logger.warning(
            "semantic cache proxy step failed", op=op, path=self._path, exc_info=error
        )
        self._count("semantic_cache_proxy_errors_total", op=op)

    async def _lookup(
        self, args: tuple[object, ...], kwargs: dict[str, object]
    ) -> _Lookup | None:
        """Return the call's key and lookup result; ``None`` when the call isn't cacheable."""
        set_served_from_cache(value=False)
        try:
            namespace = self._binding.make_namespace(args, kwargs)
            if namespace is None:
                return None
            query = self._binding.make_query(args, kwargs)
        except Exception as exc:
            self._failed("key", exc)
            return None
        hit: CachedAnswer | None = None
        with _TRACER.start_as_current_span("semantic_cache.lookup") as span:
            try:
                hit = await self._cache.lookup(
                    query, namespace, min_similarity=self._min_similarity
                )
            except Exception as exc:
                self._failed("lookup", exc)
            span.set_attribute("semantic_cache.hit", hit is not None)
            span.set_attribute("semantic_cache.path", self._path)
            if hit is not None:
                span.set_attribute("semantic_cache.similarity", hit.similarity)
        found = _Lookup(query=query, namespace=namespace, hit=hit)
        self._report(found)
        return found

    def _report(self, found: _Lookup) -> None:
        """Count the lookup and publish its fact; swallow any failure."""
        saving = Decimal(0)
        try:
            saving = self._count_lookup(found)
        except Exception as exc:
            self._failed("metrics", exc)
        try:
            self._publish_fact(found, saving)
        except Exception as exc:
            self._failed("fact", exc)

    def _count_lookup(self, found: _Lookup) -> Decimal:
        """Count the hit (with its saving) or the miss; return the saving."""
        path, workspace = self._path, found.namespace.workspace_id
        if found.hit is None:
            self._count("semantic_cache_misses_total", path=path, workspace=workspace)
            return Decimal(0)
        saving = self._saving(found.hit) if self._saving is not None else Decimal(0)
        self._count("semantic_cache_hits_total", path=path, workspace=workspace)
        self._count(
            "semantic_cache_saving_usd_total",
            float(saving),
            path=path,
            workspace=workspace,
        )
        return saving

    def _bin(self, similarity: float) -> str:
        """Return the lower edge of the bin ``similarity`` falls in (``<edge`` below the first)."""
        edges = [edge for edge in self._bins if similarity >= edge]
        return f"{max(edges):.2f}" if edges else f"<{self._bins[0]:.2f}"

    def _publish_fact(self, found: _Lookup, saving: Decimal) -> None:
        """Publish the lookup's ``cache_lookups`` fact (no-op without a publisher or an org)."""
        if self._facts is None:
            return
        dims = dict(self._fact_dimensions()) if self._fact_dimensions is not None else {}
        org_id = dims.pop("org_id", "")
        if not org_id:
            return
        hit = found.hit
        self._facts.publish([
            Fact(
                cube=CACHE_LOOKUPS_CUBE,
                org_id=org_id,
                ts=datetime.now(UTC),
                dims={
                    "outcome": "miss" if hit is None else "hit",
                    "similarity_bin": "" if hit is None else self._bin(hit.similarity),
                    "model": found.namespace.embedding_model,
                    "workspace": found.namespace.workspace_id,
                    "path": self._path,
                    **dims,
                },
                measures={"lookups": 1.0, "saving_usd": float(saving)},
                idempotency_key=str(uuid.uuid4()),
            )
        ])

    async def _hit(self, call: CallArgs, answer: object, hit: CachedAnswer) -> object:
        """Run the hit hook, mark the call served from cache and return the answer to serve.

        Raises:
            AppError: any coded error from the hook: its verdict on the hit (``FORBIDDEN``
                for a blocked answer; under enforced audit, the scan or record failure).
                An uncoded failure is swallowed and the hit served as is.

        """
        served = answer
        if self._on_hit is not None:
            try:
                replaced = await self._on_hit(call[0], call[1], answer, hit)
            except AppError:
                raise
            except Exception as exc:
                self._failed("on_hit", exc)
            else:
                served = answer if replaced is None else replaced
        set_served_from_cache(value=True)
        return served

    async def _store(self, found: _Lookup, payload: Callable[[], bytes]) -> None:
        """Write ``payload()`` through under the call's key; swallow any failure."""
        try:
            await self._cache.store(
                found.query, payload(), found.namespace, ttl_seconds=self._ttl
            )
        except Exception as exc:
            self._failed("store", exc)

    def _wrap_call(self, attr: Callable[..., object]) -> object:
        """Wrap the unary bound method: hit → decoded answer; miss → inner, then write through."""

        @functools.wraps(attr)
        async def call(*args: object, **kwargs: object) -> object:
            found = await self._lookup(args, kwargs)
            if found is not None and found.hit is not None:
                try:
                    answer = self._binding.decode(found.hit.payload)
                except Exception as exc:
                    self._failed("decode", exc)
                else:
                    return await self._hit((args, kwargs), answer, found.hit)
            result = await cast("Callable[..., Awaitable[object]]", attr)(*args, **kwargs)
            if found is not None:
                await self._store(found, lambda: self._binding.encode(result))
            return result

        return call

    def _wrap_stream(
        self,
        attr: Callable[..., object],
        source: Callable[..., object],
        *,
        keep_usage: bool,
    ) -> object:
        """Wrap a streamed bound method: hit → replay; miss → tee the inner stream, store at the end."""

        @functools.wraps(attr)
        async def stream(
            *args: object, **kwargs: object
        ) -> AsyncIterator[str | StreamUsage]:
            found = await self._lookup(args, kwargs)
            cached = self._decode_text(found)
            if cached is not None and found is not None and found.hit is not None:
                text, model = cached
                served = await self._hit(
                    (args, kwargs),
                    LLMResponse(
                        content=text,
                        model=model,
                        input_tokens=0,
                        output_tokens=0,
                        finish_reason="cache_hit",
                    ),
                    found.hit,
                )
                if isinstance(served, LLMResponse):
                    text = served.content
                else:
                    self._failed(
                        "on_hit",
                        AppError(
                            ErrorCode.INTERNAL,
                            f"stream hit replacement is {type(served).__name__}, "
                            "not an LLMResponse",
                        ),
                    )
                return self._replay(text, model, keep_usage=keep_usage)
            opener = cast(
                "Callable[..., Awaitable[AsyncIterator[str | StreamUsage]]]", source
            )
            items = await opener(*args, **kwargs)
            return self._tee(items, found, keep_usage=keep_usage)

        return stream

    def _decode_text(self, found: _Lookup | None) -> tuple[str, str] | None:
        """Return a hit's cached text and model, or ``None`` for a miss or an undecodable entry."""
        decode_text = self._binding.decode_text
        if found is None or found.hit is None or decode_text is None:
            return None
        try:
            return decode_text(found.hit.payload)
        except Exception as exc:
            self._failed("decode", exc)
            return None

    @staticmethod
    def _replay(
        text: str, model: str, *, keep_usage: bool
    ) -> AsyncIterator[str | StreamUsage]:
        """Return ``text`` replayed as a stream, ending in a zero-token usage when kept."""

        async def items() -> AsyncIterator[str | StreamUsage]:  # noqa: RUF029 - the stream port is async
            for start in range(0, len(text), _REPLAY_CHUNK_CHARS):
                yield text[start : start + _REPLAY_CHUNK_CHARS]
            if keep_usage:
                yield StreamUsage(
                    model=model,
                    input_tokens=0,
                    output_tokens=0,
                    finish_reason="cache_hit",
                )

        return items()

    async def _tee(
        self,
        items: AsyncIterator[str | StreamUsage],
        found: _Lookup | None,
        *,
        keep_usage: bool,
    ) -> AsyncIterator[str | StreamUsage]:
        """Re-yield ``items``; once fully consumed without error, write the answer through."""
        parts: list[str] = []
        usage: StreamUsage | None = None
        async for item in items:
            if isinstance(item, StreamUsage):
                usage = item
                if not keep_usage:
                    continue
            else:
                parts.append(item)
            yield item
        encode_text = self._binding.encode_text
        if found is not None and encode_text is not None:
            await self._store(found, lambda: encode_text("".join(parts), usage))
