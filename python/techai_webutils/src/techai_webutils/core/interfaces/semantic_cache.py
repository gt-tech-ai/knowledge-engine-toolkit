"""``SemanticCache`` — a similarity-keyed answer cache, partitioned by namespace.

A lookup returns a cached answer whose query is semantically close (cosine similarity at
or above a threshold) to the asked one, but only within the same ``CacheNamespace``: the
workspace, the embedding space and an optional clearance scope. The cache extends
``CacheInvalidator`` so a namespace can be evicted by prefix like any other cache tier.

Both operations are **fail-open**: a backend outage makes ``lookup`` miss and ``store`` a
no-op; neither raises into the query path.

A backend implements two primitives, ``exact`` (the normalized repeat) and ``nearest``
(the top-1 neighbour with its similarity, unthresholded); ``lookup`` composes them. The
match tiers (``CacheTier``), chained by a ``CacheLookup``, decide which candidate is
served; a ``CacheVerifier`` confirms a borderline neighbour and a ``CacheAdmission`` keeps
time- and user-specific questions out of the cache.
"""

from __future__ import annotations

import hashlib
from abc import ABC, abstractmethod
from contextvars import ContextVar
from dataclasses import dataclass
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.cache import CacheInvalidator

if TYPE_CHECKING:
    from collections.abc import Sequence

_TAG_WIDTH = 16
"""Hex characters of the namespace tag (64 bits of sha256)."""


@dataclass(frozen=True, slots=True)
class CacheNamespace:
    """The non-query partition a cached answer lives in."""

    workspace_id: str
    """The tenant partition: an answer never crosses workspaces. It is a key segment, so
    it may not contain the separator ``:``."""
    embedding_model: str
    """The embedder's model id: vectors from different models are not comparable."""
    embedding_version: str
    """The embedding-space version (the model's dimension or a deploy-time revision)."""
    scope: str = ""
    """An optional narrower partition, e.g. the requester's clearance level."""

    def __post_init__(self) -> None:
        """Reject a workspace id that would span key segments.

        Raises:
            AppValueError: ``workspace_id`` contains ``:``, so evicting one workspace by
                its ``<workspace>:`` prefix could take another's keys with it.

        """
        if ":" in self.workspace_id:
            msg = (
                f"cache namespace workspace_id may not contain ':': {self.workspace_id!r}"
            )
            raise AppValueError(msg)

    def tag(self) -> str:
        """Return the namespace's stable tag: sha256 hex (16 chars) of the four fields.

        Returns:
            The namespace's stable tag: sha256 hex (16 chars) of the four fields.

        """
        sep = "\x1f"
        joined = (
            f"{self.workspace_id}{sep}{self.embedding_model}{sep}"
            f"{self.embedding_version}{sep}{self.scope}"
        )
        return hashlib.sha256(joined.encode()).hexdigest()[:_TAG_WIDTH]


@dataclass(frozen=True, slots=True)
class CachedAnswer:
    """A cache hit."""

    payload: bytes
    """The cached answer, as encoded by the caller."""
    similarity: float
    """Cosine similarity between the asked and the cached query (1.0 for an exact
    repeat)."""
    source_query: str
    """The query the answer was cached under."""
    embedding_model: str
    """The embedder model that keyed the hit."""
    tier: str = ""
    """The match tier that served the hit (``exact``, ``semantic``, ``verified``); empty
    when no tier chain was involved."""


class SemanticCache(CacheInvalidator):
    """A similarity-keyed, namespace-partitioned answer cache (fail-open)."""

    @abstractmethod
    async def exact(self, query: str, namespace: CacheNamespace) -> CachedAnswer | None:
        """Return the answer stored under the normalized ``query`` (similarity 1.0).

        Else ``None``.

        No embedding is computed. Fail-open: a backend error is a miss, never an
        exception.
        """

    @abstractmethod
    async def nearest(self, query: str, namespace: CacheNamespace) -> CachedAnswer | None:
        """Return the closest stored answer with its similarity, whatever it is.

        Else ``None``.

        The caller applies any threshold. Fail-open: a backend error is a miss.
        """

    async def lookup(
        self, query: str, namespace: CacheNamespace, *, min_similarity: float
    ) -> CachedAnswer | None:
        """Return the exact repeat.

        Else the nearest answer at or above ``min_similarity``.

        Fail-open through the primitives: a backend error is a miss, never an exception.

        Returns:
            The exact repeat.

        """
        hit = await self.exact(query, namespace)
        if hit is not None:
            return hit
        nearest = await self.nearest(query, namespace)
        if nearest is None or nearest.similarity < min_similarity:
            return None
        return nearest

    @abstractmethod
    async def store(
        self, query: str, payload: bytes, namespace: CacheNamespace, *, ttl_seconds: int
    ) -> None:
        """Cache ``payload`` under ``query`` in ``namespace`` for ``ttl_seconds``.

        Fail-open: a backend error is a no-op, never an exception.
        """


class CacheCandidates(ABC):
    """One lookup's candidates, each fetched at most once and shared by every tier."""

    @abstractmethod
    async def exact(self) -> CachedAnswer | None:
        """Return the exact-repeat candidate (memoized), else ``None``."""

    @abstractmethod
    async def nearest(self) -> CachedAnswer | None:
        """Return the nearest-neighbour candidate with its similarity (memoized).

        Else ``None``.
        """


class CacheTier(ABC):
    """One match rule: decides whether a lookup's candidates hold an answer to serve."""

    @property
    @abstractmethod
    def name(self) -> str:
        """The tier's kind, recorded on the hit it serves (``exact``, ``semantic``, …)."""

    @abstractmethod
    async def match(
        self, query: str, candidates: CacheCandidates, *, min_similarity: float
    ) -> CachedAnswer | None:
        """Return the candidate this tier serves for ``query``, else ``None`` (fail-open).

        ``min_similarity`` is the lookup's direct-serve threshold (a per-call override or
        the tier's configured value).
        """


class CacheLookup(ABC):
    """An ordered chain of tiers over one cache: the first tier that serves wins."""

    @abstractmethod
    async def lookup(
        self,
        query: str,
        namespace: CacheNamespace,
        *,
        min_similarity: float | None = None,
    ) -> CachedAnswer | None:
        """Return the first tier's hit (``tier`` set), else ``None``; fail-open.

        ``min_similarity`` overrides the configured direct-serve threshold for this call
        (a per-workspace setting); ``None`` keeps each tier's configured value.
        """


class CacheVerifier(ABC):
    """Confirms that a borderline neighbour asks the same thing as the new question."""

    @abstractmethod
    async def same_question(self, cached_query: str, query: str) -> bool:
        """Return whether ``query`` asks what ``cached_query`` asked.

        ``False`` on any failure.
        """


class CacheAdmission(ABC):
    """Decides whether a question may be looked up in, and stored to, the cache."""

    @abstractmethod
    def admits(self, query: str, extra_phrases: Sequence[str] = ()) -> bool:
        """Return whether ``query`` is cacheable.

        ``extra_phrases`` are a tenant's never-cache phrases, added to the configured
        rules; they match as literal text, never as patterns.
        """


_SERVED_FROM_CACHE: ContextVar[bool] = ContextVar("served_from_cache", default=False)
"""Whether the current call's answer came from the semantic cache (an output, never an
input)."""


def served_from_cache() -> bool:
    """Return whether the current call was answered from the semantic cache.

    The cache decorator sets it per bound call; an outer decorator (token accounting)
    reads it after the call returns, so a hit is recorded as a zero-token usage rather
    than billed.

    Returns:
        Whether the current call was answered from the semantic cache.

    """
    return _SERVED_FROM_CACHE.get()


def set_served_from_cache(*, value: bool) -> None:
    """Mark the current call as answered (``True``) or not.

    (``False``) from the semantic cache.
    """
    _SERVED_FROM_CACHE.set(value)
