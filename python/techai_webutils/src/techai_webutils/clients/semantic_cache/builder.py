"""Config-selected ``SemanticCache`` factory (the ``clients/token_ledger/builder.py`` pattern).

``SemanticCacheConfig.kind`` selects ``stub`` (always miss, the default) or ``redis`` (the injected
KV ``Cache`` for payloads + the ``EmbeddingProvider`` + a RediSearch KNN index on the app's Redis).
Collaborators are injected, never built here. An unknown kind raises ``AppValueError``; ``redis``
without its collaborators raises ``AppError(INVALID_INPUT)``. The Redis backend is imported lazily.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.semantic_cache.stub import StubSemanticCache
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from redis.asyncio import Redis

    from techai_webutils.core.interfaces.cache import Cache
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.semantic_cache import SemanticCache


class SemanticCacheKind(StrEnum):
    """The semantic cache backends."""

    STUB = "stub"
    """Always miss (the default; zero infrastructure)."""
    REDIS = "redis"
    """KV payloads + a RediSearch cosine KNN index on the same Redis."""
    # QDRANT reserved: a vector-store-backed index, when a deployment has no RediSearch.


@dataclass(frozen=True, slots=True)
class SemanticCacheConfig:
    """Semantic cache configuration."""

    kind: str = SemanticCacheKind.STUB
    """A ``SemanticCacheKind`` value."""
    min_similarity: float = 0.95
    """The cosine similarity a cached query must reach to count as a hit."""
    ttl_seconds: int = 3600
    """How long a cached answer lives."""
    index_prefix: str = "sc"
    """Key prefix for the payload keys, index documents and the index name."""
    normalize: bool = True
    """Lower-case and whitespace-collapse queries before the exact-repeat fast path."""


def semantic_cache_from_config(
    config: SemanticCacheConfig,
    *,
    cache: Cache | None = None,
    embedding: EmbeddingProvider | None = None,
    redis: Redis | None = None,
    metrics: MetricsProvider | None = None,
) -> SemanticCache:
    """Build the ``SemanticCache`` selected by ``config.kind``.

    Args:
        config: The semantic cache configuration.
        cache: The KV cache holding payloads (``redis`` kind).
        embedding: The embedder keying similarity (``redis`` kind).
        redis: The app's async Redis client, hosting the KNN index (``redis`` kind).
        metrics: Counts backend errors for the ``redis`` kind.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``redis`` with a collaborator missing.

    """
    if config.kind == SemanticCacheKind.STUB:
        return StubSemanticCache()
    if config.kind != SemanticCacheKind.REDIS:
        msg = f"unknown semantic cache kind: {config.kind!r}"
        raise AppValueError(msg)
    if cache is None or embedding is None or redis is None:
        raise AppError(
            ErrorCode.INVALID_INPUT,
            "redis semantic cache requires cache, embedding and redis",
        )
    _ = metrics  # consumed by the redis backend
    raise AppError(
        ErrorCode.INVALID_INPUT, "redis semantic cache backend is not built yet"
    )
