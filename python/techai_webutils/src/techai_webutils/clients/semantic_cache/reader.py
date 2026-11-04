"""``SemanticCacheReader`` — one read-through step a decorator or a workflow drives by hand.

The ``SemanticCacheProxy`` wraps a single model method; a cache placed elsewhere (a whole query
workflow, keyed before retrieval) needs the same three steps without the method wrapper:

1. ``read`` — resets ``served_from_cache()``, looks the query up through the ``CacheLookup`` tier
   chain inside a ``semantic_cache.lookup`` span (``hit``, ``path``, ``similarity``, ``tier``), counts
   ``semantic_cache_lookups_total`` plus ``…_hits_total`` (labelled with the serving ``tier``,
   with ``…_saving_usd_total``) or ``…_misses_total``, and publishes one ``cache_lookups`` fact;
2. ``serve`` — awaits ``on_hit`` with the call's arguments, the decoded answer and the hit (it may
   return a replacement, and refuses the hit by raising a coded ``AppError``), then sets
   ``served_from_cache()``;
3. ``write`` — stores the encoded answer under the read's key, with a per-call TTL when given.

Every failure but the hook's coded refusal is logged, counted
(``semantic_cache_proxy_errors_total{op}``) and swallowed: the reader never fails a query. The
lookup counters are labelled ``org_id`` (from ``fact_dimensions``), ``path`` and ``workspace``; the
first lookup of a label set creates its hit (per tier), miss and saving series at 0, so
``increase()`` counts their first increment.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass
from datetime import UTC, datetime
from decimal import Decimal
from typing import TYPE_CHECKING

from opentelemetry import trace

from techai_webutils.clients.semantic_cache.tiers import CacheTierKind
from techai_webutils.core.errors import AppError
from techai_webutils.core.interfaces.semantic_cache import set_served_from_cache
from techai_webutils.core.types.fact import Fact
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Mapping

    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.semantic_cache import (
        CachedAnswer,
        CacheLookup,
        CacheNamespace,
        SemanticCache,
    )

    type HitHook = Callable[
        [tuple[object, ...], dict[str, object], object, CachedAnswer],
        Awaitable[object | None],
    ]
    """Awaited before a hit is served with the call's args, kwargs, the decoded answer and the
    hit; returns the answer to serve instead, or ``None`` to serve it as is."""

CACHE_LOOKUPS_CUBE = "cache_lookups"
"""The analytics cube each lookup lands in."""

_TRACER = trace.get_tracer(__name__)
"""Emits the ``semantic_cache.lookup`` span."""

_LOOKUP_LABELS = ["org_id", "path", "workspace"]
"""Labels on the lookup, miss and saving counters."""

_HIT_LABELS = [*_LOOKUP_LABELS, "tier"]
"""Labels on the hit counter: the lookup labels plus the tier that served the hit."""

_ERRORS = "semantic_cache_proxy_errors_total"
"""The swallowed-failure counter (named for the proxy that first emitted it)."""


@dataclass(frozen=True, slots=True)
class CacheRead:
    """One read's key and what it found."""

    query: str
    """The text the read was keyed by."""
    namespace: CacheNamespace
    """The read's namespace."""
    hit: CachedAnswer | None
    """The hit (``tier`` set), or ``None`` for a miss or a failed lookup."""


class SemanticCacheReader:
    """Read, serve and write one cached answer; fail-open apart from the hook's refusal."""

    def __init__(
        self,
        cache: SemanticCache,
        lookup: CacheLookup,
        *,
        ttl_seconds: int,
        path: str,
        metrics: MetricsProvider | None = None,
        facts: FactPublisher | None = None,
        fact_dimensions: Callable[[], Mapping[str, str]] | None = None,
        saving: Callable[[CachedAnswer], Decimal] | None = None,
        on_hit: HitHook | None = None,
        similarity_bins: tuple[float, ...] = (0.9, 0.95, 0.98, 1.0),
    ) -> None:
        """Bind the backend, the tier chain and the telemetry.

        Args:
            cache: The backend answers are written to.
            lookup: The tier chain answers are read through.
            ttl_seconds: How long a written answer lives unless ``write`` overrides it.
            path: The metric label for this placement (``generation``, ``answer``, ``retrieval``).
            metrics: Emits the lookup, hit, miss, saving and error counters; ``None`` emits none.
            facts: Receives one ``cache_lookups`` fact per read; ``None`` publishes none.
            fact_dimensions: Returns the product dimensions (must include ``org_id``).
            saving: Returns the US dollars a hit avoided (``0`` without it).
            on_hit: Awaited by ``serve`` (e.g. the audit); it may return a replacement answer,
                and any coded ``AppError`` it raises refuses the hit.
            similarity_bins: The ascending lower edges a hit's similarity is binned by.

        """
        self._cache = cache
        self._lookup = lookup
        self._ttl = ttl_seconds
        self._path = path
        self._facts = facts
        self._fact_dimensions = fact_dimensions
        self._saving = saving
        self._on_hit = on_hit
        self._bins = similarity_bins
        self._logger = get_logger("semantic_cache_reader")
        self._counters: dict[str, MetricCounter] = {}
        self._seen: set[tuple[str, ...]] = set()
        if metrics is not None:
            for name, help_text, labels in (
                (
                    "semantic_cache_lookups_total",
                    "Semantic cache lookups (hits plus misses).",
                    _LOOKUP_LABELS,
                ),
                (
                    "semantic_cache_hits_total",
                    "Semantic cache hits, by tier.",
                    _HIT_LABELS,
                ),
                ("semantic_cache_misses_total", "Semantic cache misses.", _LOOKUP_LABELS),
                (
                    "semantic_cache_saving_usd_total",
                    "US dollars avoided by hits.",
                    _LOOKUP_LABELS,
                ),
                (_ERRORS, "Swallowed semantic cache read-through errors.", ["op"]),
            ):
                self._counters[name] = metrics.counter(name, help_text, labels)

    @property
    def path(self) -> str:
        """The placement label this reader reports under."""
        return self._path

    def failed(self, op: str, error: BaseException) -> None:
        """Log and count a swallowed failure of ``op`` (also used by the proxy's decode step)."""
        self._logger.warning(
            "semantic cache read-through step failed",
            op=op,
            path=self._path,
            exc_info=error,
        )
        self._count(_ERRORS, op=op)

    async def read(
        self,
        query: str,
        namespace: CacheNamespace,
        *,
        min_similarity: float | None = None,
    ) -> CacheRead:
        """Look ``query`` up, report it, and return what was found (a failure is a miss).

        Args:
            query: The text to look up.
            namespace: The partition to look it up in.
            min_similarity: A per-call direct-serve threshold (a tenant setting); ``None`` keeps
                each tier's configured value.

        Returns:
            The read.

        """
        set_served_from_cache(value=False)
        hit: CachedAnswer | None = None
        with _TRACER.start_as_current_span("semantic_cache.lookup") as span:
            try:
                hit = await self._lookup.lookup(
                    query, namespace, min_similarity=min_similarity
                )
            except Exception as exc:
                self.failed("lookup", exc)
            span.set_attribute("semantic_cache.hit", hit is not None)
            span.set_attribute("semantic_cache.path", self._path)
            if hit is not None:
                span.set_attribute("semantic_cache.similarity", hit.similarity)
                span.set_attribute("semantic_cache.tier", hit.tier)
        found = CacheRead(query=query, namespace=namespace, hit=hit)
        self._report(found)
        return found

    async def serve(
        self,
        read: CacheRead,
        args: tuple[object, ...],
        kwargs: dict[str, object],
        answer: object,
    ) -> object:
        """Run the hit hook, mark the call served from cache and return the answer to serve.

        Args:
            read: The read whose hit is being served.
            args: The cached call's positional arguments (for the hook).
            kwargs: The cached call's keyword arguments (for the hook).
            answer: The decoded cached answer.

        Returns:
            The hook's replacement, or ``answer``.

        Raises:
            AppError: any coded error from the hook, which refuses the hit. An uncoded failure
                is swallowed and the hit served as is.

        """
        served = answer
        if self._on_hit is not None and read.hit is not None:
            try:
                replaced = await self._on_hit(args, kwargs, answer, read.hit)
            except AppError:
                raise
            except Exception as exc:
                self.failed("on_hit", exc)
            else:
                served = answer if replaced is None else replaced
        set_served_from_cache(value=True)
        return served

    async def write(
        self, read: CacheRead, payload: bytes, *, ttl_seconds: int | None = None
    ) -> None:
        """Store ``payload`` under the read's key; a failure is counted, never raised.

        Args:
            read: The read the answer is stored for.
            payload: The encoded answer.
            ttl_seconds: How long it lives; the reader's TTL when ``None``.

        """
        try:
            await self._cache.store(
                read.query,
                payload,
                read.namespace,
                ttl_seconds=self._ttl if ttl_seconds is None else ttl_seconds,
            )
        except Exception as exc:
            self.failed("store", exc)

    def _count(self, name: str, value: float = 1.0, **labels: str) -> None:
        """Increment the counter ``name`` (no-op without metrics)."""
        counter = self._counters.get(name)
        if counter is not None:
            counter.inc(value, **labels)

    def _report(self, found: CacheRead) -> None:
        """Count the read and publish its fact; swallow any failure."""
        dims: dict[str, str] = {}
        try:
            if self._fact_dimensions is not None:
                dims = dict(self._fact_dimensions())
        except Exception as exc:
            self.failed("fact", exc)
        saving = Decimal(0)
        try:
            saving = self._count_read(found, dims.get("org_id", ""))
        except Exception as exc:
            self.failed("metrics", exc)
        try:
            self._publish_fact(found, saving, dims)
        except Exception as exc:
            self.failed("fact", exc)

    def _count_read(self, found: CacheRead, org_id: str) -> Decimal:
        """Count the read and its hit (with the saving) or miss under ``org_id``; return the saving."""
        labels = {
            "org_id": org_id,
            "path": self._path,
            "workspace": found.namespace.workspace_id,
        }
        self._materialize(labels)
        self._count("semantic_cache_lookups_total", 1.0, **labels)
        if found.hit is None:
            self._count("semantic_cache_misses_total", 1.0, **labels)
            return Decimal(0)
        saving = self._saving(found.hit) if self._saving is not None else Decimal(0)
        self._count("semantic_cache_hits_total", 1.0, **labels, tier=found.hit.tier)
        self._count("semantic_cache_saving_usd_total", float(saving), **labels)
        return saving

    def _materialize(self, labels: dict[str, str]) -> None:
        """Create a label set's hit (per tier), miss and saving series at 0 the first time.

        A Prometheus counter series exists only once it is incremented, so its first sample is
        already 1 and ``increase()`` reads it as no change. A hit always follows the miss that
        stored its answer, so creating the hit series at 0 on that miss lets the first hit count.
        """
        key = tuple(labels[name] for name in _LOOKUP_LABELS)
        if key in self._seen:
            return
        self._seen.add(key)
        for tier in CacheTierKind:
            self._count("semantic_cache_hits_total", 0.0, **labels, tier=tier.value)
        for name in ("semantic_cache_misses_total", "semantic_cache_saving_usd_total"):
            self._count(name, 0.0, **labels)

    def _bin(self, similarity: float) -> str:
        """Return the lower edge of the bin ``similarity`` falls in (``<edge`` below the first)."""
        edges = [edge for edge in self._bins if similarity >= edge]
        return f"{max(edges):.2f}" if edges else f"<{self._bins[0]:.2f}"

    def _publish_fact(
        self, found: CacheRead, saving: Decimal, dims: dict[str, str]
    ) -> None:
        """Publish the read's ``cache_lookups`` fact (no-op without a publisher or an org)."""
        if self._facts is None:
            return
        dims = dict(dims)
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
