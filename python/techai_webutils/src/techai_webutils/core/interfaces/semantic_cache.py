"""``SemanticCache`` — a similarity-keyed answer cache, partitioned by namespace.

A lookup returns a cached answer whose query is semantically close (cosine similarity at or above a
threshold) to the asked one, but only within the same ``CacheNamespace``: the workspace, the
embedding space and an optional clearance scope. The cache extends ``CacheInvalidator`` so a
namespace can be evicted by prefix like any other cache tier.

Both operations are **fail-open**: a backend outage makes ``lookup`` miss and ``store`` a no-op;
neither raises into the query path.
"""

from __future__ import annotations

import hashlib
from abc import abstractmethod
from dataclasses import dataclass

from techai_webutils.core.interfaces.cache import CacheInvalidator

_TAG_WIDTH = 16
"""Hex characters of the namespace tag (64 bits of sha256)."""


@dataclass(frozen=True, slots=True)
class CacheNamespace:
    """The non-query partition a cached answer lives in."""

    workspace_id: str
    """The tenant partition: an answer never crosses workspaces."""
    embedding_model: str
    """The embedder's model id: vectors from different models are not comparable."""
    embedding_version: str
    """The embedding-space version (the model's dimension or a deploy-time revision)."""
    scope: str = ""
    """An optional narrower partition, e.g. the requester's clearance level."""

    def tag(self) -> str:
        """Return the namespace's stable tag: sha256 hex (16 chars) of the four fields."""
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
    """Cosine similarity between the asked and the cached query (1.0 for an exact repeat)."""
    source_query: str
    """The query the answer was cached under."""
    embedding_model: str
    """The embedder model that keyed the hit."""


class SemanticCache(CacheInvalidator):
    """A similarity-keyed, namespace-partitioned answer cache (fail-open)."""

    @abstractmethod
    async def lookup(
        self, query: str, namespace: CacheNamespace, *, min_similarity: float
    ) -> CachedAnswer | None:
        """Return the closest cached answer at or above ``min_similarity``, else ``None``.

        Fail-open: a backend error is a miss, never an exception.
        """

    @abstractmethod
    async def store(
        self, query: str, payload: bytes, namespace: CacheNamespace, *, ttl_seconds: int
    ) -> None:
        """Cache ``payload`` under ``query`` in ``namespace`` for ``ttl_seconds``.

        Fail-open: a backend error is a no-op, never an exception.
        """
