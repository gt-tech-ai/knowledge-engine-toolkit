"""``SemanticCacheProxy`` — a read-through semantic cache around one model method.

A method-aware ``__getattr__`` proxy: only the binding's ``method`` (unary) and, when set,
its ``stream_method`` (``stream_with_usage``; ``stream`` is served by the same wrapper)
are cached; every other attribute passes through. Per bound call it:

1. resets ``served_from_cache()``, then derives the query and the ``CacheNamespace`` from
   the call's arguments (a ``None`` namespace passes through);
2. reads it through a ``SemanticCacheReader`` (the injected ``CacheLookup`` tier chain, or
   the backend's own ``lookup`` at ``min_similarity`` when none is given), which owns the
   ``semantic_cache.lookup`` span, the counters and the ``cache_lookups`` fact;
3. **hit** — returns the decoded answer without calling the inner (a stream replays the
   cached text, then a zero-token ``StreamUsage`` with ``finish_reason="cache_hit"``),
   counts ``semantic_cache_hits_total`` and the injected ``saving``, awaits ``on_hit``
   with the answer (an ``LLMResponse`` for a stream) before serving it, then sets
   ``served_from_cache()``. The hook may return a replacement answer (e.g. redacted; a
   stream replays only an ``LLMResponse`` replacement) and refuses the hit by raising any
   coded ``AppError`` (a safety block, or an enforced audit that could not scan or
   record), which propagates;
4. **miss** — calls the inner and writes the answer through with the TTL (a stream only
   once it was fully consumed without error, so a truncated answer is never cached).

Everything else the cache, the hook or the telemetry raise is logged, counted
(``semantic_cache_proxy_errors_total{op}``) and swallowed: apart from the hook's refusal
or replacement, the proxy can never fail or change the answer. Every lookup counts
``semantic_cache_lookups_total`` plus ``…_hits_total`` (labelled with the serving
``tier``, with ``…_saving_usd_total``) or ``…_misses_total``, each labelled ``org_id``
(from ``fact_dimensions``; empty without one), ``path`` and ``workspace``, so a hit rate
is hits over lookups for one org. The first lookup of a label set creates its hit (per
tier), miss and saving series at 0, so ``increase()`` counts their first increment.
``enabled=False`` or a ``StubSemanticCache`` makes it a passthrough. Each lookup publishes
one ``cache_lookups`` fact (outcome, similarity bin, model, workspace; lookups, saving).
"""

from __future__ import annotations

import asyncio
import dataclasses
import functools
from dataclasses import dataclass
from typing import TYPE_CHECKING, cast, final, override

from techai_webutils.clients.semantic_cache.reader import (
    CACHE_LOOKUPS_CUBE,
    CacheRead,
    SemanticCacheReader,
    SemanticCacheReaderOptions,
)
from techai_webutils.clients.semantic_cache.stub import StubSemanticCache
from techai_webutils.clients.semantic_cache.tiers import CacheTierKind
from techai_webutils.core.interfaces.llm import LLMResponse, StreamUsage
from techai_webutils.core.interfaces.semantic_cache import (
    CacheLookup,
    set_served_from_cache,
)
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable, Mapping
    from decimal import Decimal

    from techai_webutils.clients.semantic_cache.reader import HitHook
    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.semantic_cache import (
        CachedAnswer,
        CacheNamespace,
        SemanticCache,
    )

    type CallArgs = tuple[tuple[object, ...], dict[str, object]]
    """A bound call's positional and keyword arguments."""

logger = get_logger(__name__)

__all__ = ["CACHE_LOOKUPS_CUBE", "SemanticCacheBinding", "SemanticCacheProxy"]

_REPLAY_CHUNK_CHARS = 64
"""Characters per chunk when a cached answer is replayed as a stream."""


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
    """The streamed method cached (``stream_with_usage``); ``None`` streams pass
    through."""
    encode_text: Callable[[str, StreamUsage | None], bytes] | None = None
    """Encodes a fully streamed answer (its text and terminal usage) for the cache."""
    decode_text: Callable[[bytes], tuple[str, str]] | None = None
    """Returns a cached payload's text and model, for a streamed replay."""


@final
class _BackendLookup(CacheLookup):
    """The backend's own ``lookup`` at a fixed threshold.

    For a proxy given no tier chain.
    """

    def __init__(self, cache: SemanticCache, min_similarity: float) -> None:
        """Bind the backend and the proxy's configured threshold.

        Args:
            cache: The semantic cache backend.
            min_similarity: The proxy's configured threshold.

        """
        self._cache = cache
        self._min_similarity = min_similarity

    @override
    async def lookup(
        self,
        query: str,
        namespace: CacheNamespace,
        *,
        min_similarity: float | None = None,
    ) -> CachedAnswer | None:
        """Return the backend's hit, its ``tier`` set from its similarity when blank.

        Args:
            query: The query.
            namespace: The tenant and model scope the answer must belong to.
            min_similarity: A threshold for this lookup; ``None`` uses the configured one.

        Returns:
            The backend's hit, its ``tier`` set from its similarity when blank.

        """
        hit = await self._cache.lookup(
            query,
            namespace,
            min_similarity=self._min_similarity
            if min_similarity is None
            else min_similarity,
        )
        if hit is None or hit.tier:
            return hit
        tier = CacheTierKind.EXACT if hit.similarity >= 1.0 else CacheTierKind.SEMANTIC
        return dataclasses.replace(hit, tier=tier.value)


@dataclass(frozen=True, kw_only=True)
class SemanticCacheProxyOptions:
    """What ``SemanticCacheProxy`` takes, grouped into one value."""

    min_similarity: float
    """The lowest similarity that counts as a hit when no ``lookup`` is given (a tier
    chain carries its own thresholds).
    """

    ttl_seconds: int
    """How long a stored answer lives."""

    enabled: bool
    """``False`` makes the proxy a passthrough."""

    path: str
    """The metric label for this placement (``generation``, ``retrieval``)."""

    metrics: MetricsProvider | None = None
    """Emits hit / miss / saving / error counters; ``None`` emits nothing."""

    facts: FactPublisher | None = None
    """Receives one ``cache_lookups`` fact per lookup; ``None`` publishes nothing."""

    fact_dimensions: Callable[[], Mapping[str, str]] | None = None
    """Returns the product dimensions (must include ``org_id``)."""

    saving: Callable[[CachedAnswer], Decimal] | None = None
    """Returns the US dollars a hit avoided (``0`` without it)."""

    on_hit: HitHook | None = None
    """Awaited before each hit is served (e.g. the audit); it may return a replacement
    answer. Any coded ``AppError`` it raises refuses the hit; an uncoded failure is
    swallowed and counted.
    """

    similarity_bins: tuple[float, ...] = (0.9, 0.95, 0.98, 1.0)
    """The ascending lower edges a hit's similarity is binned by."""

    lookup: CacheLookup | None = None
    """The tier chain answers are read through (``cache_lookup_from_config``); ``None``
    reads through ``cache.lookup`` at ``min_similarity``.
    """


@final
class SemanticCacheProxy:
    """Serves repeat model calls from a semantic cache; fail-open."""

    def __init__(
        self,
        inner: object,
        cache: SemanticCache,
        binding: SemanticCacheBinding,
        options: SemanticCacheProxyOptions,
    ) -> None:
        """Wrap ``inner``'s bound methods with ``cache``.

        Args:
            inner: The provider or generator whose calls are cached.
            cache: The semantic cache answers are written to.
            binding: Which methods are cached and how.
            options: The settings and collaborators, grouped.

        """
        self._inner = inner
        self._binding = binding
        self._active = options.enabled and not isinstance(cache, StubSemanticCache)
        self._reader = SemanticCacheReader(
            cache,
            options.lookup
            if options.lookup is not None
            else _BackendLookup(cache, options.min_similarity),
            options=SemanticCacheReaderOptions(
                ttl_seconds=options.ttl_seconds,
                path=options.path,
                metrics=options.metrics,
                facts=options.facts,
                fact_dimensions=options.fact_dimensions,
                saving=options.saving,
                on_hit=options.on_hit,
                similarity_bins=options.similarity_bins,
            ),
        )

    def __getattr__(self, name: str) -> object:
        """Return the inner attribute, cached when it is a bound method.

        Args:
            name: The attribute.

        Returns:
            The inner attribute, cached when it is a bound method.

        """
        attr = cast("object", getattr(self._inner, name))
        if not callable(attr) or not self._active:
            return attr
        stream_method = self._binding.stream_method
        if name == self._binding.method:
            return self._wrap_call(attr)
        if stream_method is not None and name in {stream_method, "stream"}:
            source = cast("Callable[..., object]", getattr(self._inner, stream_method))
            return self._wrap_stream(attr, source, keep_usage=name == stream_method)
        return attr

    async def _lookup(
        self, args: tuple[object, ...], kwargs: dict[str, object]
    ) -> CacheRead | None:
        """Return the call's read; ``None`` when the call isn't cacheable.

        Args:
            args: The call's positional arguments.
            kwargs: The call's keyword arguments.

        Returns:
            The call's read; ``None`` when the call isn't cacheable.

        """
        set_served_from_cache(value=False)
        try:
            namespace = self._binding.make_namespace(args, kwargs)
            if namespace is None:
                return None
            query = self._binding.make_query(args, kwargs)
        except Exception:
            logger.exception("lookup failed")
            self._reader.failed("key")
            return None
        return await self._reader.read(query, namespace)

    async def _hit(self, call: CallArgs, answer: object, found: CacheRead) -> object:
        """Serve ``found``'s hit through the reader's hook and return the answer to serve.

        A callee raises ``AppError``: any coded error from the hook: its verdict on the
        hit (``FORBIDDEN`` for a blocked answer; under enforced audit, the scan or record
        failure). An uncoded failure is swallowed and the hit served as is.

        Args:
            call: The call's arguments.
            answer: The cached answer.
            found: The lookup's result.

        Returns:
            The served answer.

        """
        return await self._reader.serve(found, call[0], call[1], answer)

    async def _store(self, found: CacheRead, payload: Callable[[], bytes]) -> None:
        """Encode with ``payload()`` and write it through; swallow any failure.

        Args:
            found: The lookup's result, carrying the key.
            payload: Encodes the answer.

        """
        try:
            encoded = payload()
        except Exception:
            logger.exception("store failed")
            self._reader.failed("store")
            return
        await self._reader.write(found, encoded)

    def _wrap_call(self, attr: Callable[..., object]) -> object:
        """Wrap the unary bound method: hit → decoded answer; miss → inner.

        Then write through.

        Args:
            attr: The inner model call.

        Returns:
            The wrapped unary call.

        """

        @functools.wraps(attr)
        async def call(*args: object, **kwargs: object) -> object:
            found = await self._lookup(args, kwargs)
            if found is not None and found.hit is not None:
                try:
                    answer = self._binding.decode(found.hit.payload)
                except Exception:
                    logger.exception("call failed")
                    self._reader.failed("decode")
                    self._reader.refuse(found)
                else:
                    return await self._hit((args, kwargs), answer, found)
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
        """Wrap a streamed bound method: hit → replay; miss → tee the inner stream.

        Store at the end.

        Args:
            attr: The inner method the caller asked for.
            source: The inner method that yields usage too.
            keep_usage: Whether to re-yield the ``StreamUsage`` item.

        Returns:
            The wrapped streaming call.

        """

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
                    found,
                )
                if isinstance(served, LLMResponse):
                    text = served.content
                else:
                    logger.warning(
                        "stream hit replacement is not an LLMResponse",
                        served_type=type(served).__name__,
                    )
                    self._reader.failed("on_hit")
                return self._replay(text, model, keep_usage=keep_usage)
            opener = cast(
                "Callable[..., Awaitable[AsyncIterator[str | StreamUsage]]]", source
            )
            items = await opener(*args, **kwargs)
            return self._tee(items, found, keep_usage=keep_usage)

        return stream

    def _decode_text(self, found: CacheRead | None) -> tuple[str, str] | None:
        """Return a hit's cached text and model.

        Or ``None`` for a miss or an undecodable entry.

        Args:
            found: The lookup's result, if any.

        Returns:
            A hit's cached text and model.

        """
        decode_text = self._binding.decode_text
        if found is None or found.hit is None or decode_text is None:
            return None
        try:
            return decode_text(found.hit.payload)
        except Exception:
            logger.exception("decode_text failed")
            self._reader.failed("decode")
            self._reader.refuse(found)
            return None

    @staticmethod
    def _replay(
        text: str, model: str, *, keep_usage: bool
    ) -> AsyncIterator[str | StreamUsage]:
        """Return ``text`` replayed as a stream.

        Ending in a zero-token usage when kept.

        Args:
            text: The cached answer.
            model: The model that wrote it.
            keep_usage: Whether to yield a ``StreamUsage`` item last.

        Returns:
            ``text`` replayed as a stream.

        """

        async def items() -> AsyncIterator[str | StreamUsage]:
            for start in range(0, len(text), _REPLAY_CHUNK_CHARS):
                await asyncio.sleep(0)
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
        found: CacheRead | None,
        *,
        keep_usage: bool,
    ) -> AsyncIterator[str | StreamUsage]:
        """Re-yield ``items``; once fully consumed without error.

        Write the answer through.

        Args:
            items: The inner stream.
            found: The lookup's result; ``None`` stores nothing.
            keep_usage: Whether to re-yield the ``StreamUsage`` item.

        Yields:
            Each item of ``items``.

        """
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
