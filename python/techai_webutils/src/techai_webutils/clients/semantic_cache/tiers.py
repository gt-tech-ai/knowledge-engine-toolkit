"""Config-selected match tiers over a ``SemanticCache``, chained by a ``TieredLookup``.

A tier decides whether a lookup's candidates hold an answer to serve:

- ``exact`` — the normalized repeat (no embedding);
- ``semantic`` — the nearest neighbour at or above the direct-serve threshold;
- ``verified`` — the nearest neighbour in the band ``[floor, threshold)``, served only when a
  ``CacheVerifier`` confirms it asks the same question within ``timeout_seconds``.

``cache_lookup_from_config`` builds the chain from an ordered list of ``CacheTierConfig``; the
first tier that serves wins and the hit records it in ``CachedAnswer.tier``. The tiers share one
memoized ``CacheCandidates`` per lookup, so ``semantic`` and ``verified`` cost one embedding and
one vector search together. A per-call ``min_similarity`` (a tenant setting) replaces every
tier's configured threshold, so 1.0 means "exact repeats only" and the band's ceiling follows
it. The chain is fail-open: any error is a miss.
"""

from __future__ import annotations

import asyncio
import dataclasses
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.semantic_cache import (
    CacheCandidates,
    CacheLookup,
    CacheTier,
)
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.semantic_cache import (
        CachedAnswer,
        CacheNamespace,
        CacheVerifier,
        SemanticCache,
    )


class CacheTierKind(StrEnum):
    """The match tiers a lookup chain can list."""

    EXACT = "exact"
    """The normalized repeat, without an embedding."""
    SEMANTIC = "semantic"
    """The nearest neighbour at or above the direct-serve threshold."""
    VERIFIED = "verified"
    """The nearest neighbour in ``[floor, threshold)``, confirmed by a ``CacheVerifier``."""


@dataclass(frozen=True, slots=True)
class CacheTierConfig:
    """One entry of the ordered tier list."""

    kind: str
    """A ``CacheTierKind`` value."""
    min_similarity: float = 0.95
    """The direct-serve threshold (``semantic``) or the band's ceiling (``verified``), in (0, 1]."""
    floor: float = 0.88
    """The lowest similarity the ``verified`` band sends to the verifier."""
    timeout_seconds: float = 2.0
    """How long the ``verified`` tier waits for the verifier before treating it as a miss."""


class _Candidates(CacheCandidates):
    """One lookup's candidates, each fetched from the backend at most once."""

    def __init__(
        self, cache: SemanticCache, query: str, namespace: CacheNamespace
    ) -> None:
        """Bind the backend and the lookup's key; nothing is fetched until a tier asks."""
        self._cache = cache
        self._query = query
        self._namespace = namespace
        self._exact: tuple[CachedAnswer | None] | None = None
        self._nearest: tuple[CachedAnswer | None] | None = None

    @override
    async def exact(self) -> CachedAnswer | None:
        """Return the exact-repeat candidate, fetching it on first use."""
        if self._exact is None:
            self._exact = (await self._cache.exact(self._query, self._namespace),)
        return self._exact[0]

    @override
    async def nearest(self) -> CachedAnswer | None:
        """Return the nearest-neighbour candidate, fetching it on first use."""
        if self._nearest is None:
            self._nearest = (await self._cache.nearest(self._query, self._namespace),)
        return self._nearest[0]


class ExactTier(CacheTier):
    """Serves the normalized repeat."""

    @property
    @override
    def name(self) -> str:
        """``exact``."""
        return CacheTierKind.EXACT

    @override
    async def match(
        self, query: str, candidates: CacheCandidates, *, min_similarity: float
    ) -> CachedAnswer | None:
        """Return the exact candidate, whatever the threshold."""
        return await candidates.exact()


class SemanticTier(CacheTier):
    """Serves the nearest neighbour at or above the threshold."""

    @property
    @override
    def name(self) -> str:
        """``semantic``."""
        return CacheTierKind.SEMANTIC

    @override
    async def match(
        self, query: str, candidates: CacheCandidates, *, min_similarity: float
    ) -> CachedAnswer | None:
        """Return the neighbour when its similarity reaches ``min_similarity``."""
        nearest = await candidates.nearest()
        if nearest is None or nearest.similarity < min_similarity:
            return None
        return nearest


class VerifiedTier(CacheTier):
    """Serves a borderline neighbour that a verifier confirms asks the same question."""

    def __init__(
        self, floor: float, verifier: CacheVerifier, *, timeout_seconds: float
    ) -> None:
        """Bind the band's floor, the verifier and how long to wait for it."""
        self._floor = floor
        self._verifier = verifier
        self._timeout = timeout_seconds
        self._logger = get_logger("semantic_cache_tiers")

    @property
    @override
    def name(self) -> str:
        """``verified``."""
        return CacheTierKind.VERIFIED

    @override
    async def match(
        self, query: str, candidates: CacheCandidates, *, min_similarity: float
    ) -> CachedAnswer | None:
        """Return a neighbour in ``[floor, min_similarity)`` the verifier confirms in time."""
        nearest = await candidates.nearest()
        if nearest is None or not self._floor <= nearest.similarity < min_similarity:
            return None
        try:
            async with asyncio.timeout(self._timeout):
                same = await self._verifier.same_question(nearest.source_query, query)
        except TimeoutError:
            self._logger.warning(
                "semantic cache verifier timed out (miss)",
                extra={"timeout_seconds": self._timeout},
            )
            return None
        return nearest if same else None


class TieredLookup(CacheLookup):
    """An ordered tier chain over one cache: the first tier that serves wins; fail-open."""

    def __init__(
        self, cache: SemanticCache, tiers: Sequence[tuple[CacheTier, float]]
    ) -> None:
        """Bind the backend and the ``(tier, configured threshold)`` pairs, in order."""
        self._cache = cache
        self._tiers = tuple(tiers)
        self._logger = get_logger("semantic_cache_tiers")

    @override
    async def lookup(
        self,
        query: str,
        namespace: CacheNamespace,
        *,
        min_similarity: float | None = None,
    ) -> CachedAnswer | None:
        """Return the first tier's hit with ``tier`` set, else ``None``; any error is a miss."""
        if not self._tiers:
            return None
        candidates = _Candidates(self._cache, query, namespace)
        try:
            for tier, configured in self._tiers:
                threshold = configured if min_similarity is None else min_similarity
                hit = await tier.match(query, candidates, min_similarity=threshold)
                if hit is not None:
                    return dataclasses.replace(hit, tier=tier.name)
        except Exception as exc:
            self._logger.warning(
                "semantic cache tier lookup failed (fail-open)", extra={"error": str(exc)}
            )
        return None


def _validate(configs: Sequence[CacheTierConfig], verifier: CacheVerifier | None) -> None:
    """Raise ``AppValueError`` for a tier list the lookup cannot run."""
    kinds = [config.kind for config in configs]
    for config in configs:
        if config.kind not in CacheTierKind.__members__.values():
            msg = f"unknown semantic cache tier kind: {config.kind!r}"
            raise AppValueError(msg)
        if not 0.0 < config.min_similarity <= 1.0:
            msg = f"tier {config.kind!r} min_similarity must be in (0, 1]: {config.min_similarity}"
            raise AppValueError(msg)
    if len(set(kinds)) != len(kinds):
        msg = f"semantic cache tier listed twice: {kinds}"
        raise AppValueError(msg)
    for config in configs:
        if config.kind != CacheTierKind.VERIFIED:
            continue
        if verifier is None:
            msg = "the verified tier requires a cache verifier"
            raise AppValueError(msg)
        if not 0.0 <= config.floor < config.min_similarity:
            msg = (
                f"verified tier floor {config.floor} must be below its ceiling "
                f"{config.min_similarity}"
            )
            raise AppValueError(msg)


def cache_lookup_from_config(
    tiers: Sequence[CacheTierConfig],
    *,
    cache: SemanticCache,
    verifier: CacheVerifier | None = None,
) -> CacheLookup:
    """Build the tier chain ``tiers`` lists, in order, over ``cache``.

    Args:
        tiers: The ordered tier list; empty builds a lookup that always misses.
        cache: The backend whose ``exact`` / ``nearest`` primitives the tiers read.
        verifier: Confirms ``verified`` band candidates (required when that tier is listed).

    Returns:
        The lookup.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown or duplicate kind, a similarity outside
            (0, 1], ``verified`` without a verifier, or a band whose floor is not below its
            ceiling.

    """
    _validate(tiers, verifier)
    chain: list[tuple[CacheTier, float]] = []
    for config in tiers:
        tier: CacheTier
        if config.kind == CacheTierKind.EXACT:
            tier = ExactTier()
        elif config.kind == CacheTierKind.SEMANTIC:
            tier = SemanticTier()
        elif verifier is not None:  # _validate refused a verified tier without a verifier
            tier = VerifiedTier(
                config.floor, verifier, timeout_seconds=config.timeout_seconds
            )
        else:
            msg = "the verified tier requires a cache verifier"
            raise AppValueError(msg)
        chain.append((tier, config.min_similarity))
    return TieredLookup(cache, chain)
