"""``SemanticCacheReader``: one read-through step.

A decorator or a workflow drives it by hand.

The ``SemanticCacheProxy`` wraps a single model method; a cache placed elsewhere (a whole
query workflow, keyed before retrieval) needs the same three steps without the method
wrapper:

1. ``read`` — resets ``served_from_cache()`` and looks the query up through the
   ``CacheLookup`` tier chain inside a ``semantic_cache.lookup`` span (``hit``, ``path``,
   ``similarity``, ``tier``). A miss is reported at once; a hit is reported only when its
   fate is known;
2. ``serve`` — awaits ``on_hit`` with the call's arguments, the decoded answer and the hit
   (it may return a replacement, and refuses the hit by raising a coded ``AppError``),
   sets ``served_from_cache()`` and reports the hit;
3. ``refuse`` — reports a hit the caller will not serve (an undecodable entry, a check of
   its own, or the hook's refusal) as a miss, so a hit that was never served counts no hit
   and no saving;
4. ``write`` — stores the encoded answer under the read's key, with a per-call TTL when
   given.

Reporting counts ``semantic_cache_lookups_total`` plus ``…_hits_total`` (labelled with the
serving ``tier``, with ``…_saving_usd_total``) or ``…_misses_total``, and publishes one
``cache_lookups`` fact. Every hit a caller reads must end in ``serve`` or ``refuse``.

Every failure but the hook's coded refusal is logged, counted
(``semantic_cache_proxy_errors_total{op}``) and swallowed: the reader never fails a query.
The lookup counters are labelled ``org_id`` (from ``fact_dimensions``), ``path`` and
``workspace``; the first lookup of a label set creates its hit (per tier), miss and saving
series at 0, so ``increase()`` counts their first increment.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass, replace
from datetime import UTC, datetime
from decimal import Decimal
from typing import TYPE_CHECKING, final

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
    """Awaited before a hit is served with the call's args, kwargs, the decoded answer and
    the hit; returns the answer to serve instead, or ``None`` to serve it as is."""

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


@dataclass(frozen=True, kw_only=True)
class SemanticCacheReaderOptions:
    """What ``SemanticCacheReader`` takes, grouped into one value."""

    ttl_seconds: int
    """How long a written answer lives unless ``write`` overrides it."""

    path: str
    """The metric label for this placement (``generation``, ``answer``, ``retrieval``)."""

    metrics: MetricsProvider | None = None
    """Emits the lookup, hit, miss, saving and error counters; ``None`` emits none."""

    facts: FactPublisher | None = None
    """Receives one ``cache_lookups`` fact per read; ``None`` publishes none."""

    fact_dimensions: Callable[[], Mapping[str, str]] | None = None
    """Returns the product dimensions (must include ``org_id``)."""

    saving: Callable[[CachedAnswer], Decimal] | None = None
    """Returns the US dollars a hit avoided (``0`` without it)."""

    on_hit: HitHook | None = None
    """Awaited by ``serve`` (e.g. the audit); it may return a replacement answer, and any
    coded ``AppError`` it raises refuses the hit.
    """

    similarity_bins: tuple[float, ...] = (0.9, 0.95, 0.98, 1.0)
    """The ascending lower edges a hit's similarity is binned by."""


@final
class SemanticCacheReader:
    """Read, serve and write one cached answer.

    Fail-open apart from the hook's refusal.
    """

    def __init__(
        self,
        cache: SemanticCache,
        lookup: CacheLookup,
        options: SemanticCacheReaderOptions,
    ) -> None:
        """Bind the backend, the tier chain and the telemetry.

        Args:
            cache: The backend answers are written to.
            lookup: The tier chain answers are read through.
            options: The settings and collaborators, grouped.

        """
        self._cache = cache
        self._lookup = lookup
        self._ttl = options.ttl_seconds
        self._path = options.path
        self._facts = options.facts
        self._fact_dimensions = options.fact_dimensions
        self._saving = options.saving
        self._on_hit = options.on_hit
        self._bins = options.similarity_bins
        self._logger = get_logger("semantic_cache_reader")
        self._counters: dict[str, MetricCounter] = {}
        self._seen: set[tuple[str, ...]] = set()
        if options.metrics is not None:
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
                self._counters[name] = options.metrics.counter(name, help_text, labels)

    @property
    def path(self) -> str:
        """The placement label this reader reports under."""
        return self._path

    def failed(self, op: str) -> None:
        """Count a swallowed failure of ``op`` (the handler has logged it).

        (also used by the proxy's decode step).

        Args:
            op: The failed operation.

        """
        self._count(_ERRORS, op=op)

    async def read(
        self,
        query: str,
        namespace: CacheNamespace,
        *,
        min_similarity: float | None = None,
    ) -> CacheRead:
        """Look ``query`` up and return what was found (a failure is a miss).

        A miss is reported here; a hit is reported by ``serve`` or ``refuse``, once the
        caller knows whether it is served.

        Args:
            query: The text to look up.
            namespace: The partition to look it up in.
            min_similarity: A per-call direct-serve threshold (a tenant setting); ``None``
                keeps each tier's configured value.

        Returns:
            What the lookup found.

        """
        set_served_from_cache(value=False)
        hit: CachedAnswer | None = None
        with _TRACER.start_as_current_span("semantic_cache.lookup") as span:
            try:
                hit = await self._lookup.lookup(
                    query, namespace, min_similarity=min_similarity
                )
            except Exception:
                self._logger.exception("read failed")
                self.failed("lookup")
            span.set_attribute("semantic_cache.hit", hit is not None)
            span.set_attribute("semantic_cache.path", self._path)
            if hit is not None:
                span.set_attribute("semantic_cache.similarity", hit.similarity)
                span.set_attribute("semantic_cache.tier", hit.tier)
        found = CacheRead(query=query, namespace=namespace, hit=hit)
        if hit is None:
            self._report(found)
        return found

    async def serve(
        self,
        read: CacheRead,
        args: tuple[object, ...],
        kwargs: dict[str, object],
        answer: object,
    ) -> object:
        """Serve a hit: run the hook, mark the call cached, report, return.

        Args:
            read: The read whose hit is being served.
            args: The cached call's positional arguments (for the hook).
            kwargs: The cached call's keyword arguments (for the hook).
            answer: The decoded cached answer.

        Returns:
            The hook's replacement, or ``answer``.

        Raises:
            AppError: any coded error from the hook, which refuses the hit (reported as a
                miss). An uncoded failure is swallowed and the hit served as is.

        """
        served = answer
        if self._on_hit is not None and read.hit is not None:
            try:
                replaced = await self._on_hit(args, kwargs, answer, read.hit)
            except AppError:
                self.refuse(read)
                raise
            except Exception:
                self._logger.exception("serve failed")
                self.failed("on_hit")
            else:
                served = answer if replaced is None else replaced
        set_served_from_cache(value=True)
        self._report(read)
        return served

    def refuse(self, read: CacheRead) -> None:
        """Report a hit the caller will not serve as a miss: no hit, no saving.

        Args:
            read: The read whose hit is refused.

        """
        self._report(replace(read, hit=None))

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
        except Exception:
            self._logger.exception("write failed")
            self.failed("store")

    def _count(self, name: str, value: float = 1.0, **labels: str) -> None:
        """Increment the counter ``name`` (no-op without metrics).

        Args:
            name: The counter.
            value: The increment.
            **labels: The series' labels.

        """
        counter = self._counters.get(name)
        if counter is not None:
            counter.inc(value, **labels)

    def _report(self, found: CacheRead) -> None:
        """Count the read and publish its fact; swallow any failure.

        Args:
            found: The lookup's result.

        """
        dims: dict[str, str] = {}
        try:
            if self._fact_dimensions is not None:
                dims = dict(self._fact_dimensions())
        except Exception:
            self._logger.exception("report failed")
            self.failed("fact")
        saving = Decimal(0)
        try:
            saving = self._count_read(found, dims.get("org_id", ""))
        except Exception:
            self._logger.exception("report failed")
            self.failed("metrics")
        try:
            self._publish_fact(found, saving, dims)
        except Exception:
            self._logger.exception("report failed")
            self.failed("fact")

    def _count_read(self, found: CacheRead, org_id: str) -> Decimal:
        """Count the read and its hit or miss under ``org_id``.

        A hit counts its saving; returns the saving.

        Args:
            found: The lookup's result.
            org_id: The tenant.

        Returns:
            The read and its hit (with the saving) or miss under ``org_id``.

        """
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
        """Create a label set's hit, miss and saving series at 0 the first time.

        Hits are per tier.

        A Prometheus counter series exists only once it is incremented, so its first
        sample is already 1 and ``increase()`` reads it as no change. A hit always follows
        the miss that stored its answer, so creating the hit series at 0 on that miss lets
        the first hit count.

        Args:
            labels: The label set.

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
        """Return the lower edge of the bin ``similarity`` falls in.

        Below the first edge it is ``<edge``.

        Args:
            similarity: The hit's similarity.

        Returns:
            The lower edge of the bin ``similarity`` falls in.

        """
        edges = [edge for edge in self._bins if similarity >= edge]
        return f"{max(edges):.2f}" if edges else f"<{self._bins[0]:.2f}"

    def _publish_fact(
        self, found: CacheRead, saving: Decimal, dims: dict[str, str]
    ) -> None:
        """Publish the read's ``cache_lookups`` fact.

        A no-op without a publisher or an org.

        Args:
            found: The lookup's result.
            saving: The cost the hit saved.
            dims: The fact's dimensions.

        """
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
